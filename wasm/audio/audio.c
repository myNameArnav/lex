// Browser-side audio conversion for Lex: decodes AC-3/E-AC-3 packets and
// re-encodes them as AAC-LC, which every Media Source Extensions
// implementation accepts. The player feeds it the packets the server copied
// out of the file, so the server never has to decode audio.
//
// One conversion at a time: la_open starts a new one (each server stream,
// i.e. each seek), la_close frees it. Output packets accumulate in a buffer
// as records of [f64 pts][i32 duration][i32 size][size bytes], with pts and
// duration in 1/sample_rate units.

#include <emscripten/emscripten.h>
#include <libavcodec/avcodec.h>
#include <libavutil/audio_fifo.h>
#include <libavutil/channel_layout.h>
#include <libavutil/mathematics.h>
#include <libavutil/opt.h>
#include <string.h>

static AVCodecContext *dec, *enc;
static AVAudioFifo *fifo;
static AVPacket *pkt;
static AVFrame *frame, *efr;
static int64_t next_pts; // pts of the FIFO's first sample, 1/sample_rate
static int bitrate;
static uint8_t *in;
static int in_cap;
static uint8_t *out;
static int out_len, out_cap;

EMSCRIPTEN_KEEPALIVE void la_close(void) {
  avcodec_free_context(&dec);
  avcodec_free_context(&enc);
  if (fifo) av_audio_fifo_free(fifo);
  fifo = NULL;
  av_packet_free(&pkt);
  av_frame_free(&frame);
  av_frame_free(&efr);
  out_len = 0;
}

// codec: 0 = AC-3, 1 = E-AC-3. timescale: the input packets' pts unit.
// channels: output channel count (2 downmixes anything larger).
EMSCRIPTEN_KEEPALIVE int la_open(int codec, int timescale, int channels, int kbps) {
  la_close();
  av_log_set_level(AV_LOG_QUIET);
  const AVCodec *c = avcodec_find_decoder(codec == 1 ? AV_CODEC_ID_EAC3 : AV_CODEC_ID_AC3);
  if (!c || !(dec = avcodec_alloc_context3(c))) return -1;
  dec->pkt_timebase = (AVRational){1, timescale};
  if (channels <= 2) {
    AVChannelLayout stereo = AV_CHANNEL_LAYOUT_STEREO;
    av_opt_set_chlayout(dec->priv_data, "downmix", &stereo, 0);
  }
  if (avcodec_open2(dec, c, NULL) < 0) return -2;
  pkt = av_packet_alloc();
  frame = av_frame_alloc();
  efr = av_frame_alloc();
  if (!pkt || !frame || !efr) return -3;
  bitrate = kbps * 1000;
  next_pts = AV_NOPTS_VALUE;
  return 0;
}

static int emit(AVPacket *p) {
  int need = out_len + 16 + p->size;
  if (need > out_cap) {
    int cap = out_cap ? out_cap : 65536;
    while (cap < need) cap *= 2;
    uint8_t *b = av_realloc(out, cap);
    if (!b) return AVERROR(ENOMEM);
    out = b;
    out_cap = cap;
  }
  double pts = p->pts;
  int32_t dur = p->duration, size = p->size;
  memcpy(out + out_len, &pts, 8);
  memcpy(out + out_len + 8, &dur, 4);
  memcpy(out + out_len + 12, &size, 4);
  memcpy(out + out_len + 16, p->data, p->size);
  out_len = need;
  return 0;
}

static int drain(void) {
  int r;
  while ((r = avcodec_receive_packet(enc, pkt)) == 0) {
    r = emit(pkt);
    av_packet_unref(pkt);
    if (r < 0) return r;
  }
  return r == AVERROR(EAGAIN) || r == AVERROR_EOF ? 0 : r;
}

static int open_encoder(const AVFrame *f) {
  const AVCodec *c = avcodec_find_encoder(AV_CODEC_ID_AAC);
  if (!c || !(enc = avcodec_alloc_context3(c))) return -10;
  enc->sample_rate = f->sample_rate;
  enc->sample_fmt = AV_SAMPLE_FMT_FLTP;
  av_channel_layout_copy(&enc->ch_layout, &f->ch_layout);
  enc->bit_rate = bitrate;
  enc->time_base = (AVRational){1, f->sample_rate};
  enc->flags |= AV_CODEC_FLAG_GLOBAL_HEADER;
  if (avcodec_open2(enc, c, NULL) < 0) return -11;
  fifo = av_audio_fifo_alloc(AV_SAMPLE_FMT_FLTP, f->ch_layout.nb_channels, enc->frame_size * 4);
  return fifo ? 0 : -12;
}

// Encode whole AAC frames from the FIFO (and the remainder when flushing).
static int encode_fifo(int flush) {
  int fs = enc->frame_size;
  while (av_audio_fifo_size(fifo) >= fs || (flush && av_audio_fifo_size(fifo) > 0)) {
    int n = FFMIN(fs, av_audio_fifo_size(fifo));
    av_frame_unref(efr);
    efr->nb_samples = n;
    efr->format = enc->sample_fmt;
    efr->sample_rate = enc->sample_rate;
    av_channel_layout_copy(&efr->ch_layout, &enc->ch_layout);
    if (av_frame_get_buffer(efr, 0) < 0) return AVERROR(ENOMEM);
    if (av_audio_fifo_read(fifo, (void **)efr->data, n) < n) return -20;
    efr->pts = next_pts;
    next_pts += n;
    int r = avcodec_send_frame(enc, efr);
    if (r < 0) return r;
    if ((r = drain()) < 0) return r;
  }
  return 0;
}

static int take_frame(AVFrame *f) {
  if (!enc) {
    int r = open_encoder(f);
    if (r < 0) return r;
  }
  if (f->sample_rate != enc->sample_rate || f->ch_layout.nb_channels != enc->ch_layout.nb_channels || f->format != AV_SAMPLE_FMT_FLTP)
    return -30; // a mid-stream format change: let the player fall back
  int64_t pts = f->pts == AV_NOPTS_VALUE ? AV_NOPTS_VALUE : av_rescale_q(f->pts, dec->pkt_timebase, enc->time_base);
  // Start, or a jump of more than 50 ms (a gap in the file): restart the
  // timeline there rather than drifting audio against video.
  int64_t expect = next_pts == AV_NOPTS_VALUE ? AV_NOPTS_VALUE : next_pts + av_audio_fifo_size(fifo);
  if (pts != AV_NOPTS_VALUE && (expect == AV_NOPTS_VALUE || llabs(pts - expect) > enc->sample_rate / 20)) {
    av_audio_fifo_reset(fifo);
    next_pts = pts;
  } else if (next_pts == AV_NOPTS_VALUE) {
    next_pts = 0;
  }
  if (av_audio_fifo_write(fifo, (void **)f->data, f->nb_samples) < f->nb_samples) return AVERROR(ENOMEM);
  return encode_fifo(0);
}

// A buffer of at least size bytes for the next packet.
EMSCRIPTEN_KEEPALIVE uint8_t *la_inbuf(int size) {
  if (size + AV_INPUT_BUFFER_PADDING_SIZE > in_cap) {
    av_free(in);
    in_cap = size + AV_INPUT_BUFFER_PADDING_SIZE;
    in = av_mallocz(in_cap);
    if (!in) in_cap = 0;
  }
  return in;
}

// Decode the packet in la_inbuf; pts is in the timescale given to la_open.
// Damaged packets are skipped, as ffmpeg does.
EMSCRIPTEN_KEEPALIVE int la_decode(int size, double pts) {
  if (!dec || !in) return -1;
  memset(in + size, 0, AV_INPUT_BUFFER_PADDING_SIZE);
  pkt->data = in;
  pkt->size = size;
  pkt->pts = pkt->dts = (int64_t)pts;
  int r = avcodec_send_packet(dec, pkt);
  pkt->data = NULL;
  pkt->size = 0;
  if (r < 0) return r == AVERROR_INVALIDDATA ? 0 : r;
  while ((r = avcodec_receive_frame(dec, frame)) == 0) {
    r = take_frame(frame);
    av_frame_unref(frame);
    if (r < 0) return r;
  }
  return r == AVERROR(EAGAIN) || r == AVERROR_INVALIDDATA ? 0 : r;
}

// End of the input: encode what's left in the FIFO and the encoder.
EMSCRIPTEN_KEEPALIVE int la_flush(void) {
  if (!enc) return 0;
  int r = encode_fifo(1);
  if (r < 0) return r;
  if ((r = avcodec_send_frame(enc, NULL)) < 0) return r;
  return drain();
}

EMSCRIPTEN_KEEPALIVE uint8_t *la_out(void) { return out; }
EMSCRIPTEN_KEEPALIVE int la_out_len(void) { return out_len; }
EMSCRIPTEN_KEEPALIVE void la_out_clear(void) { out_len = 0; }

// Encoder parameters, once the first frame has been decoded (0 before).
EMSCRIPTEN_KEEPALIVE int la_rate(void) { return enc ? enc->sample_rate : 0; }
EMSCRIPTEN_KEEPALIVE int la_channels(void) { return enc ? enc->ch_layout.nb_channels : 0; }
EMSCRIPTEN_KEEPALIVE uint8_t *la_config(void) { return enc ? enc->extradata : NULL; }
EMSCRIPTEN_KEEPALIVE int la_config_len(void) { return enc ? enc->extradata_size : 0; }
