// Browser-side Direct Stream for Lex: reads a Matroska/WebM file through the
// player's HTTP range requests and copies its video and one audio track into
// fragmented MP4, the same stream the server's ffmpeg remux writes
// (stream.buildArgs with -c copy), so the player's MSE path takes it as is
// and the server only serves file bytes.
//
// Reads are synchronous: js_read (remux-core.js) fills the buffer from a
// range request, so this runs in a Worker. rx_open opens the file once,
// rx_start (re)starts the output at a time (each seek: a fresh init
// segment), rx_pump muxes until a fragment is out. Output accumulates in a
// buffer that JS drains (rx_out, rx_out_len, rx_out_clear).

#include <emscripten/emscripten.h>
#include <libavformat/avformat.h>
#include <libavutil/mathematics.h>
#include <libavutil/opt.h>
#include <libavutil/pixfmt.h>
#include <stdio.h>
#include <string.h>

EM_JS(int, js_read, (uint8_t *buf, int size, double pos), { return Module.lexRead(buf, size, pos); });

static AVFormatContext *ic, *oc;
static AVIOContext *in_pb;
static int64_t in_pos, in_size;
static int vin = -1, ain = -1;  // input stream indexes
static int vout = -1, aout = -1; // output stream indexes
static int finished, started;
static uint8_t *out;
static int out_len, out_cap;
static char err[256];
static int frag_us = 1000000;

// Video DTS. Matroska stores presentation times only; with B-frames the
// decode times have to be rebuilt. Frames are muxed LOOKAHEAD video frames
// behind the reading, and each frame's DTS is the smallest PTS still
// queued: its rank in presentation order. That's right as long as frames
// reorder less than LOOKAHEAD deep (H.264 and HEVC allow 16), however the
// depth changes through the file. dts_lead keeps DTS before PTS; the
// muxer's negative composition offsets take it out again.
#define LOOKAHEAD 32
#define MAX_QUEUE 1024
static int64_t frame_dur, dts_lead, last_dts;
static AVPacket *queue[MAX_QUEUE];
static int qhead, qlen, qvideo, qeof, keyed;
static AVPacket *pkt;

static int fail(int code, const char *fmt, const char *what) {
  snprintf(err, sizeof err, fmt, what);
  return code;
}

static int fail_av(int code, const char *what) {
  char e[128];
  av_strerror(code, e, sizeof e);
  snprintf(err, sizeof err, "%s: %s", what, e);
  return code;
}

static int read_cb(void *opaque, uint8_t *buf, int n) {
  if (in_pos >= in_size) return AVERROR_EOF;
  if (n > in_size - in_pos) n = in_size - in_pos;
  int r = js_read(buf, n, (double)in_pos);
  if (r < 0) return AVERROR(EIO); // JS keeps the request's error
  if (r == 0) return AVERROR_EOF;
  in_pos += r;
  return r;
}

static int64_t seek_cb(void *opaque, int64_t off, int whence) {
  switch (whence & ~AVSEEK_FORCE) {
  case AVSEEK_SIZE: return in_size;
  case SEEK_SET: break;
  case SEEK_CUR: off += in_pos; break;
  case SEEK_END: off += in_size; break;
  default: return AVERROR(EINVAL);
  }
  if (off < 0) return AVERROR(EINVAL);
  in_pos = off;
  return off;
}

static int write_cb(void *opaque, const uint8_t *buf, int n) {
  if (out_len + n > out_cap) {
    int cap = out_cap ? out_cap : 1 << 20;
    while (cap < out_len + n) cap *= 2;
    uint8_t *b = av_realloc(out, cap);
    if (!b) return AVERROR(ENOMEM);
    out = b;
    out_cap = cap;
  }
  memcpy(out + out_len, buf, n);
  out_len += n;
  return n;
}

static void clear_queue(void) {
  for (; qlen; qlen--, qhead = (qhead + 1) % MAX_QUEUE) av_packet_free(&queue[qhead]);
  qhead = qvideo = qeof = 0;
}

static void close_output(void) {
  if (!oc) return;
  if (oc->pb) {
    av_freep(&oc->pb->buffer);
    avio_context_free(&oc->pb);
  }
  avformat_free_context(oc);
  oc = NULL;
}

EMSCRIPTEN_KEEPALIVE void rx_close(void) {
  close_output();
  clear_queue();
  avformat_close_input(&ic);
  if (in_pb) {
    av_freep(&in_pb->buffer);
    avio_context_free(&in_pb);
  }
  av_packet_free(&pkt);
  av_freep(&out);
  out_len = out_cap = 0;
  vin = ain = -1;
}

static int video_ok(enum AVCodecID id) {
  return id == AV_CODEC_ID_H264 || id == AV_CODEC_ID_HEVC || id == AV_CODEC_ID_AV1 || id == AV_CODEC_ID_VP9;
}

static int audio_ok(enum AVCodecID id) {
  return id == AV_CODEC_ID_AAC || id == AV_CODEC_ID_AC3 || id == AV_CODEC_ID_EAC3 || id == AV_CODEC_ID_FLAC ||
         id == AV_CODEC_ID_OPUS || id == AV_CODEC_ID_MP3;
}

// Opens a file of size bytes. audio is the audio stream's index (-1: none),
// frag_ms the minimum fragment duration (the server's FragmentMs).
EMSCRIPTEN_KEEPALIVE int rx_open(double size, int audio, int frag_ms) {
  rx_close();
  av_log_set_level(AV_LOG_QUIET);
  err[0] = 0;
  in_size = (int64_t)size;
  in_pos = 0;
  if (frag_ms > 0) frag_us = frag_ms * 1000;
  uint8_t *buf = av_malloc(1 << 16);
  if (!buf || !(in_pb = avio_alloc_context(buf, 1 << 16, 0, NULL, read_cb, NULL, seek_cb))) return fail(-1, "%s", "out of memory");
  if (!(ic = avformat_alloc_context())) return fail(-1, "%s", "out of memory");
  ic->pb = in_pb;
  ic->flags |= AVFMT_FLAG_CUSTOM_IO;
  int r = avformat_open_input(&ic, NULL, av_find_input_format("matroska"), NULL);
  if (r < 0) return fail_av(r, "Reading the file");
  for (unsigned i = 0; i < ic->nb_streams; i++) {
    AVStream *st = ic->streams[i];
    AVCodecParameters *par = st->codecpar;
    if (vin < 0 && par->codec_type == AVMEDIA_TYPE_VIDEO && !(st->disposition & AV_DISPOSITION_ATTACHED_PIC)) vin = i;
    else if ((int)i == audio && par->codec_type == AVMEDIA_TYPE_AUDIO) ain = i;
  }
  for (unsigned i = 0; i < ic->nb_streams; i++) {
    AVStream *st = ic->streams[i];
    if ((int)i == vin || (int)i == ain) continue;
    // Skip everything else (subtitles, other audio) and free attachments
    // (fonts) the demuxer read in whole.
    st->discard = AVDISCARD_ALL;
    if (st->codecpar->codec_type == AVMEDIA_TYPE_ATTACHMENT) {
      av_freep(&st->codecpar->extradata);
      st->codecpar->extradata_size = 0;
    }
  }
  if (vin < 0) return fail(-2, "%s", "the file has no video");
  if (!video_ok(ic->streams[vin]->codecpar->codec_id))
    return fail(-2, "unsupported video codec %s", avcodec_get_name(ic->streams[vin]->codecpar->codec_id));
  if (audio >= 0 && ain < 0) return fail(-2, "%s", "the audio track is missing");
  if (ain >= 0 && !audio_ok(ic->streams[ain]->codecpar->codec_id))
    return fail(-2, "unsupported audio codec %s", avcodec_get_name(ic->streams[ain]->codecpar->codec_id));
  if (!(pkt = av_packet_alloc())) return fail(-1, "%s", "out of memory");
  started = 0;
  return 0;
}

// Reads the next packet of the two streams into p; 0 or AVERROR_EOF/error.
static int read_packet(AVPacket *p) {
  for (;;) {
    int r = av_read_frame(ic, p);
    if (r < 0) return r;
    if (p->stream_index == vin || p->stream_index == ain) return 0;
    av_packet_unref(p);
  }
}

// Reads until LOOKAHEAD video frames wait behind the next one (or the file
// ends).
static int refill(void) {
  while (!qeof && qlen < MAX_QUEUE && qvideo <= LOOKAHEAD) {
    AVPacket *p = av_packet_alloc();
    if (!p) return AVERROR(ENOMEM);
    int r = read_packet(p);
    if (r < 0) {
      av_packet_free(&p);
      if (r != AVERROR_EOF) return r;
      qeof = 1;
      break;
    }
    if (p->stream_index == vin) {
      // A start (seek) lands on a keyframe; nothing before it can be decoded.
      if (!keyed && !(p->flags & AV_PKT_FLAG_KEY)) {
        av_packet_free(&p);
        continue;
      }
      if (p->pts == AV_NOPTS_VALUE) {
        av_packet_free(&p);
        return fail(-5, "%s", "video frames without timestamps");
      }
      keyed = 1;
      qvideo++;
    }
    queue[(qhead + qlen++) % MAX_QUEUE] = p;
  }
  return 0;
}

// The frame duration (for frames without one) and the DTS lead: at least
// 2 s, and 17 frames for slow frame rates.
static void measure(void) {
  int64_t v[LOOKAHEAD + 1];
  int n = 0;
  for (int i = 0; i < qlen && n <= LOOKAHEAD; i++) {
    AVPacket *p = queue[(qhead + i) % MAX_QUEUE];
    if (p->stream_index == vin) v[n++] = p->pts;
  }
  int64_t longest = 0;
  frame_dur = 0;
  for (int i = 0; i < n; i++) {
    int64_t next = 0; // the next PTS after v[i]
    for (int j = 0; j < n; j++)
      if (v[j] > v[i] && (!next || v[j] < next)) next = v[j];
    if (!next) continue;
    if (!frame_dur || next - v[i] < frame_dur) frame_dur = next - v[i];
    if (next - v[i] > longest) longest = next - v[i];
  }
  if (!frame_dur) frame_dur = 1;
  AVRational tb = ic->streams[vin]->time_base;
  dts_lead = FFMAX(av_rescale_q(2, (AVRational){1, 1}, tb), 17 * longest);
}

static int64_t video_dts(int64_t pts) {
  int64_t first = pts;
  for (int i = 0; i < qlen; i++) {
    AVPacket *p = queue[(qhead + i) % MAX_QUEUE];
    if (p->stream_index == vin && p->pts < first) first = p->pts;
  }
  int64_t dts = first - dts_lead;
  if (last_dts != AV_NOPTS_VALUE && dts <= last_dts) dts = last_dts + 1;
  last_dts = dts;
  return dts;
}

// matroskadec drops VP9's CodecPrivate, but the MP4 muxer's vpcC box needs
// the profile, bit depth and chroma subsampling: read them from the first
// keyframe's uncompressed header (VP9 bitstream spec 6.2).
typedef struct {
  const uint8_t *b;
  int n, at;
} Bits;

static int bits(Bits *g, int k) {
  int v = 0;
  for (; k > 0; k--, g->at++) {
    if (g->at >= g->n * 8) return -1;
    v = v << 1 | (g->b[g->at >> 3] >> (7 - (g->at & 7)) & 1);
  }
  return v;
}

static void vp9_params(AVCodecParameters *par, const AVPacket *p) {
  static const enum AVPixelFormat yuv[3][2][2] = { // [depth][subsampled x][subsampled y]
    {{AV_PIX_FMT_YUV444P, AV_PIX_FMT_YUV440P}, {AV_PIX_FMT_YUV422P, AV_PIX_FMT_YUV420P}},
    {{AV_PIX_FMT_YUV444P10, AV_PIX_FMT_YUV440P10}, {AV_PIX_FMT_YUV422P10, AV_PIX_FMT_YUV420P10}},
    {{AV_PIX_FMT_YUV444P12, AV_PIX_FMT_YUV440P12}, {AV_PIX_FMT_YUV422P12, AV_PIX_FMT_YUV420P12}},
  };
  static const enum AVPixelFormat rgb[3] = {AV_PIX_FMT_GBRP, AV_PIX_FMT_GBRP10, AV_PIX_FMT_GBRP12};
  static const enum AVColorSpace spaces[8] = {AVCOL_SPC_UNSPECIFIED, AVCOL_SPC_BT470BG, AVCOL_SPC_BT709, AVCOL_SPC_SMPTE170M,
                                              AVCOL_SPC_SMPTE240M, AVCOL_SPC_BT2020_NCL, AVCOL_SPC_UNSPECIFIED, AVCOL_SPC_RGB};
  Bits g = {p->data, p->size, 0};
  if (bits(&g, 2) != 2) return; // frame marker
  int profile = bits(&g, 1);
  profile |= bits(&g, 1) << 1;
  if (profile == 3) bits(&g, 1);
  // Not a shown keyframe: show_existing_frame, frame_type,
  // (show_frame, error_resilient_mode), the sync code.
  if (bits(&g, 1) != 0 || bits(&g, 1) != 0 || bits(&g, 2) < 0 || bits(&g, 24) != 0x498342) return;
  int depth = profile >= 2 ? 1 + bits(&g, 1) : 0; // 8, 10 or 12 bits
  int space = bits(&g, 3), full = 1, sx = 0, sy = 0;
  if (space < 0 || depth < 0) return;
  if (space != 7) {
    full = bits(&g, 1);
    sx = sy = 1;
    if (profile & 1) {
      sx = bits(&g, 1);
      sy = bits(&g, 1);
    }
    if (full < 0 || sx < 0 || sy < 0) return;
  }
  par->format = space == 7 ? rgb[depth] : yuv[depth][sx][sy];
  par->profile = profile;
  if (par->color_range == AVCOL_RANGE_UNSPECIFIED) par->color_range = full ? AVCOL_RANGE_JPEG : AVCOL_RANGE_MPEG;
  if (par->color_space == AVCOL_SPC_UNSPECIFIED) par->color_space = spaces[space];
}

// Starts the output at t seconds: the keyframe at or before it, with a new
// init segment. Timestamps stay the file's, as with ffmpeg -copyts.
EMSCRIPTEN_KEEPALIVE int rx_start(double t) {
  if (!ic) return fail(-1, "%s", "not open");
  close_output();
  clear_queue();
  out_len = 0;
  finished = 0;
  err[0] = 0;
  AVStream *vs = ic->streams[vin];
  // A failed range request (a lost connection) left the reader failed.
  in_pb->error = 0;
  in_pb->eof_reached = 0;
  // The first start at 0 just carries on from the header; anything else
  // seeks through the file's index (Cues).
  if (t > 0 || started) {
    int64_t ts = av_rescale_q((int64_t)(t * AV_TIME_BASE), AV_TIME_BASE_Q, vs->time_base);
    int r = av_seek_frame(ic, vin, ts, AVSEEK_FLAG_BACKWARD);
    if (r < 0) return fail_av(r, "Seeking");
  }
  started = 1;
  keyed = 0;
  last_dts = AV_NOPTS_VALUE;
  int r = refill();
  if (r < 0) return err[0] ? r : fail_av(r, "Reading the file");
  measure();

  r = avformat_alloc_output_context2(&oc, NULL, "mp4", NULL);
  if (r < 0) return fail_av(r, "Starting the MP4 muxer");
  int ids[2] = {vin, ain};
  for (int i = 0; i < 2; i++) {
    if (ids[i] < 0) continue;
    AVStream *is = ic->streams[ids[i]], *os = avformat_new_stream(oc, NULL);
    if (!os) return fail(-1, "%s", "out of memory");
    if ((r = avcodec_parameters_copy(os->codecpar, is->codecpar)) < 0) return fail_av(r, "Copying codec parameters");
    os->codecpar->codec_tag = is->codecpar->codec_id == AV_CODEC_ID_HEVC ? MKTAG('h', 'v', 'c', '1') : 0;
    os->time_base = is->time_base;
    if (i == 0) vout = os->index;
    else aout = os->index;
  }
  if (ic->streams[vin]->codecpar->codec_id == AV_CODEC_ID_VP9) {
    for (int i = 0; i < qlen; i++) {
      AVPacket *p = queue[(qhead + i) % MAX_QUEUE];
      if (p->stream_index == vin) {
        vp9_params(oc->streams[vout]->codecpar, p);
        break;
      }
    }
  }
  uint8_t *buf = av_malloc(1 << 16);
  if (!buf || !(oc->pb = avio_alloc_context(buf, 1 << 16, 1, NULL, NULL, write_cb, NULL))) return fail(-1, "%s", "out of memory");
  // The server's movflags (stream.buildArgs): (E-)AC-3 sample entries are
  // built from the first packet, so the moov waits for it. Plus negative
  // composition offsets: B-frames start decoding before the first frame
  // shows, and without them that delay is what moves video later than
  // audio (MSE ignores the edit list that would undo it).
  enum AVCodecID aid = ain >= 0 ? ic->streams[ain]->codecpar->codec_id : AV_CODEC_ID_NONE;
  AVDictionary *opts = NULL;
  av_dict_set(&opts, "movflags", "+frag_keyframe+empty_moov+default_base_moof+frag_discont+negative_cts_offsets", 0);
  if (aid == AV_CODEC_ID_AC3 || aid == AV_CODEC_ID_EAC3) av_dict_set(&opts, "movflags", "+delay_moov", AV_DICT_APPEND);
  av_dict_set_int(&opts, "min_frag_duration", frag_us, 0);
  av_dict_set(&opts, "avoid_negative_ts", "disabled", 0);
  r = avformat_write_header(oc, &opts);
  av_dict_free(&opts);
  if (r < 0) return fail_av(r, "Writing the MP4 header");
  avio_flush(oc->pb);
  return 0;
}

// The next packet to mux, with the video DTS set.
static int next_packet(AVPacket *p) {
  int r = refill();
  if (r < 0) return r;
  if (!qlen) return AVERROR_EOF;
  AVPacket *q = queue[qhead];
  if (q->stream_index == vin) {
    q->dts = video_dts(q->pts); // before it leaves the queue
    qvideo--;
  }
  av_packet_move_ref(p, q);
  av_packet_free(&queue[qhead]);
  qhead = (qhead + 1) % MAX_QUEUE;
  qlen--;
  return 0;
}

// Muxes until some output is ready (a fragment, usually) or the file ends.
// Returns 0, 1 once the end is written, or a negative error (rx_error).
EMSCRIPTEN_KEEPALIVE int rx_pump(void) {
  if (!oc) return fail(-1, "%s", "not started");
  if (finished) return 1;
  while (out_len == 0) {
    int r = next_packet(pkt);
    if (r == AVERROR_EOF) {
      if ((r = av_write_trailer(oc)) < 0) return fail_av(r, "Finishing the MP4");
      avio_flush(oc->pb);
      finished = 1;
      return 1;
    }
    if (r < 0) return err[0] ? r : fail_av(r, "Reading the file");
    AVStream *is = ic->streams[pkt->stream_index];
    int video = pkt->stream_index == vin;
    if (video) {
      if (pkt->dts > pkt->pts) {
        av_packet_unref(pkt);
        return fail(-5, "%s", "frames reordered deeper than the lookahead");
      }
      if (pkt->duration <= 0) pkt->duration = frame_dur;
    } else {
      if (pkt->pts == AV_NOPTS_VALUE) pkt->pts = pkt->dts;
      if (pkt->pts == AV_NOPTS_VALUE) {
        av_packet_unref(pkt);
        continue;
      }
      pkt->dts = pkt->pts;
    }
    AVStream *os = oc->streams[video ? vout : aout];
    pkt->stream_index = os->index;
    pkt->pos = -1;
    av_packet_rescale_ts(pkt, is->time_base, os->time_base);
    r = av_interleaved_write_frame(oc, pkt);
    av_packet_unref(pkt);
    if (r < 0) return fail_av(r, "Writing the MP4");
    avio_flush(oc->pb);
  }
  return 0;
}

EMSCRIPTEN_KEEPALIVE uint8_t *rx_out(void) { return out; }
EMSCRIPTEN_KEEPALIVE int rx_out_len(void) { return out_len; }
EMSCRIPTEN_KEEPALIVE void rx_out_clear(void) { out_len = 0; }
EMSCRIPTEN_KEEPALIVE const char *rx_error(void) { return err; }
