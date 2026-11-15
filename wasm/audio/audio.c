// Browser-side audio conversion for Lex: decodes AC-3/E-AC-3, DTS (core,
// DTS-HD HRA/MA) and Dolby TrueHD/MLP packets and re-encodes them as AAC-LC,
// which every Media Source Extensions implementation accepts. The player
// feeds it the packets the server copied out of the file, so the server
// never has to decode audio.
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
#include <libswresample/swresample.h>
#include <string.h>

// la_open's codec argument.
static const enum AVCodecID codecs[] = {AV_CODEC_ID_AC3, AV_CODEC_ID_EAC3, AV_CODEC_ID_DTS, AV_CODEC_ID_TRUEHD, AV_CODEC_ID_MLP};

static AVCodecContext *dec, *enc;
static SwrContext *swr; // set once a decoded frame isn't already what the encoder takes
static AVAudioFifo *fifo;
static AVPacket *pkt;
static AVFrame *frame, *cfr, *efr;
static int64_t next_pts; // pts of the FIFO's first sample, 1/sample_rate
static int bitrate, max_channels;
static uint8_t *in;
static int in_cap;
static uint8_t *out;
static int out_len, out_cap;

EMSCRIPTEN_KEEPALIVE void la_close(void) {
  avcodec_free_context(&dec);
  avcodec_free_context(&enc);
  swr_free(&swr);
  if (fifo) av_audio_fifo_free(fifo);
  fifo = NULL;
  av_packet_free(&pkt);
  av_frame_free(&frame);
  av_frame_free(&cfr);
  av_frame_free(&efr);
  out_len = 0;
}

// codec: an index into codecs. timescale: the input packets' pts unit.
// channels: the most output channels (2 mixes anything larger down).
EMSCRIPTEN_KEEPALIVE int la_open(int codec, int timescale, int channels, int kbps) {
  la_close();
  av_log_set_level(AV_LOG_QUIET);
  if (codec < 0 || codec >= (int)(sizeof codecs / sizeof codecs[0])) return -1;
  const AVCodec *c = avcodec_find_decoder(codecs[codec]);
  if (!c || !(dec = avcodec_alloc_context3(c))) return -1;
  dec->pkt_timebase = (AVRational){1, timescale};
  // Mixing down in the decoder uses the stream's own coefficients (AC-3,
  // DTS) or picks the matching presentation (TrueHD carries 2.0 and 5.1
  // alongside 7.1). The AC-3 decoder only mixes to stereo; swresample
  // handles whatever the decoder leaves.
  AVChannelLayout stereo = AV_CHANNEL_LAYOUT_STEREO, surround = AV_CHANNEL_LAYOUT_5POINT1;
  if (channels <= 2) av_opt_set_chlayout(dec->priv_data, "downmix", &stereo, 0);
  else if (channels == 6 && codecs[codec] != AV_CODEC_ID_AC3 && codecs[codec] != AV_CODEC_ID_EAC3)
    av_opt_set_chlayout(dec->priv_data, "downmix", &surround, 0);
  if (avcodec_open2(dec, c, NULL) < 0) return -2;
  pkt = av_packet_alloc();
  frame = av_frame_alloc();
  cfr = av_frame_alloc();
  efr = av_frame_alloc();
  if (!pkt || !frame || !cfr || !efr) return -3;
  bitrate = kbps * 1000;
  max_channels = channels;
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

static int aac_takes(const AVCodec *c, const AVChannelLayout *l) {
  const AVChannelLayout *ls;
  int n;
  if (avcodec_get_supported_config(NULL, c, AV_CODEC_CONFIG_CHANNEL_LAYOUT, 0, (const void **)&ls, &n) < 0 || !ls) return 0;
  for (int i = 0; i < n; i++)
    if (!av_channel_layout_compare(&ls[i], l)) return 1;
  return 0;
}

// The encoder takes the first decoded frame's layout, if it fits in the
// channel limit and AAC has it (not DTS-ES 6.1, say), else the standard
// layout for that many channels; and its rate, up to 48 kHz.
static int open_encoder(const AVFrame *f) {
  const AVCodec *c = avcodec_find_encoder(AV_CODEC_ID_AAC);
  if (!c || !(enc = avcodec_alloc_context3(c))) return -10;
  int n = FFMIN(f->ch_layout.nb_channels, max_channels);
  if (n == f->ch_layout.nb_channels && aac_takes(c, &f->ch_layout)) {
    av_channel_layout_copy(&enc->ch_layout, &f->ch_layout);
  } else {
    av_channel_layout_default(&enc->ch_layout, n);
    if (!aac_takes(c, &enc->ch_layout)) {
      av_channel_layout_uninit(&enc->ch_layout);
      enc->ch_layout = n >= 6 ? (AVChannelLayout)AV_CHANNEL_LAYOUT_5POINT1 : (AVChannelLayout)AV_CHANNEL_LAYOUT_STEREO;
    }
  }
  enc->sample_rate = FFMIN(f->sample_rate, 48000);
  enc->sample_fmt = AV_SAMPLE_FMT_FLTP;
  enc->bit_rate = bitrate;
  enc->time_base = (AVRational){1, enc->sample_rate};
  enc->flags |= AV_CODEC_FLAG_GLOBAL_HEADER;
  if (avcodec_open2(enc, c, NULL) < 0) return -11;
  fifo = av_audio_fifo_alloc(AV_SAMPLE_FMT_FLTP, enc->ch_layout.nb_channels, enc->frame_size * 4);
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

static int fifo_write(AVFrame *f) {
  if (f->nb_samples && av_audio_fifo_write(fifo, (void **)f->data, f->nb_samples) < f->nb_samples) return AVERROR(ENOMEM);
  return 0;
}

// Convert f (NULL: what swresample still holds) into the FIFO.
static int resample(const AVFrame *f) {
  av_frame_unref(cfr);
  cfr->format = enc->sample_fmt;
  cfr->sample_rate = enc->sample_rate;
  av_channel_layout_copy(&cfr->ch_layout, &enc->ch_layout);
  int r = swr_convert_frame(swr, cfr, f);
  return r < 0 ? r : fifo_write(cfr);
}

static int take_frame(AVFrame *f) {
  int r;
  if (!enc && (r = open_encoder(f)) < 0) return r;
  // Anything the encoder doesn't take as is goes through swresample, which
  // follows the decoder through mid-stream changes too (DTS-HD MA starts
  // on its lossy core after a seek, a TV channel switches 2.0 to 5.1).
  int direct = f->format == enc->sample_fmt && f->sample_rate == enc->sample_rate && !av_channel_layout_compare(&f->ch_layout, &enc->ch_layout);
  if (!direct && !swr && !(swr = swr_alloc())) return AVERROR(ENOMEM);
  int64_t pts = f->pts == AV_NOPTS_VALUE ? AV_NOPTS_VALUE : av_rescale_q(f->pts, dec->pkt_timebase, enc->time_base);
  // swresample's buffered samples come out first.
  int64_t held = swr && swr_is_initialized(swr) ? swr_get_delay(swr, enc->sample_rate) : 0;
  // Start, or a jump of more than 50 ms (a gap in the file): restart the
  // timeline there rather than drifting audio against video.
  int64_t expect = next_pts == AV_NOPTS_VALUE ? AV_NOPTS_VALUE : next_pts + av_audio_fifo_size(fifo) + held;
  if (pts != AV_NOPTS_VALUE && (expect == AV_NOPTS_VALUE || llabs(pts - expect) > enc->sample_rate / 20)) {
    av_audio_fifo_reset(fifo);
    if (held) swr_close(swr); // drop audio from before the gap
    next_pts = pts;
  } else if (next_pts == AV_NOPTS_VALUE) {
    next_pts = 0;
  }
  if (!swr) r = fifo_write(f);
  else {
    r = resample(f);
    if (r == AVERROR_INPUT_CHANGED) {
      // New input format: flush the old conversion, then start over.
      if ((r = resample(NULL)) >= 0) {
        swr_close(swr);
        r = resample(f);
      }
    }
  }
  return r < 0 ? r : encode_fifo(0);
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

// End of the input: encode what's left in swresample, the FIFO and the
// encoder.
EMSCRIPTEN_KEEPALIVE int la_flush(void) {
  if (!enc) return 0;
  int r;
  if (swr && swr_is_initialized(swr) && (r = resample(NULL)) < 0) return r;
  if ((r = encode_fifo(1)) < 0) return r;
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
