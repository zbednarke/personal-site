#include <Arduino.h>
#include <ESP_I2S.h>
#include <esp_rom_gpio.h>
#include <soc/gpio_sig_map.h>
#include <memory>
#include <new>
#include "standalone.h"

// Keep room for the I2S read buffer and HTTP reply decoder.
SET_LOOP_TASK_STACK_SIZE(12288);

constexpr uint8_t EYE_PIN = 18;
constexpr uint32_t PULSE_MS = 1800;
constexpr uint32_t VOICE_TIMEOUT_MS = 800;
constexpr uint8_t AMP_BCLK = 26;
constexpr uint8_t AMP_LRCLK = 25;
constexpr uint8_t AMP_DIN = 27;
constexpr uint32_t AUDIO_RATE = 24000;
constexpr uint32_t USB_BAUD = 921600;
constexpr size_t DMA_FRAMES = 240;
constexpr size_t DMA_BUFFERS = 10;
constexpr size_t PCM_MAX_BYTES = 8 * DMA_FRAMES * 3 / 2;
constexpr uint8_t DEFAULT_SPEAKER_GAIN = 90;
constexpr uint8_t MIC_SCK = 32;
constexpr uint8_t MIC_WS = 33;
constexpr uint8_t MIC_SD = 34;
constexpr uint32_t MIC_RATE = 16000;
constexpr size_t MIC_BLOCK_FRAMES = 320;

enum class EyeMode { Idle, On, Off, Pulse, Voice };
EyeMode eyeMode = EyeMode::Idle;
uint32_t pulseStartedAt = 0;
uint32_t lastVoiceAt = 0;
uint8_t voiceBrightness = 0;
String command;
i2s_chan_handle_t speaker = nullptr;
uint8_t pcm[PCM_MAX_BYTES];
size_t pcmLength = 0;
size_t pcmReceived = 0;
uint32_t lastPCMAt = 0;
volatile uint32_t playedAudio = 0;
uint32_t lastPlayedAudio = 0;
float audioEnvelope = 0;
uint8_t speakerGain = DEFAULT_SPEAKER_GAIN;
I2SClass mic;
bool micActive = false;
uint32_t lastMicReadAt = 0;

// Report the level of each DMA buffer as its samples leave I2S. PWM itself
// runs in the main loop, including while the next packet is arriving.
bool IRAM_ATTR audioSent(i2s_chan_handle_t, i2s_event_data_t *event, void *) {
  const auto *samples = static_cast<const int16_t *>(event->dma_buf);
  uint32_t energy = 0;
  const size_t count = event->size / sizeof(int16_t);
  for (size_t i = 0; i < count; i += 2) {
    const int32_t sample = samples[i] >> 4;
    energy += sample * sample;
  }
  const uint32_t meanSquare = energy / (count / 2);
  playedAudio = ((playedAudio + (1u << 24)) & 0xff000000u) | meanSquare;
  return false;
}

void updateSpeakerEye() {
  const uint32_t played = playedAudio;
  if (played == lastPlayedAudio) return;
  lastPlayedAudio = played;
  const float rms = sqrtf(played & 0xffffffu) / (2048.0f * speakerGain / 100.0f);
  const float target = powf(constrain((rms - 0.006f) / 0.15f, 0.0f, 1.0f), 0.8f);
  audioEnvelope += (target - audioEnvelope) * (target > audioEnvelope ? 0.65f : 0.30f);
  voiceBrightness = static_cast<uint8_t>(roundf(255 * audioEnvelope));
}

void parkSpeakerPins() {
  // An enabled MAX98357A should see no clock or data while the mic is active.
  for (uint8_t pin : {AMP_BCLK, AMP_LRCLK, AMP_DIN}) {
    esp_rom_gpio_connect_out_signal(pin, SIG_GPIO_OUT_IDX, false, false);
    pinMode(pin, OUTPUT);
    digitalWrite(pin, LOW);
  }
}

void stopSpeaker() {
  if (!speaker) return;
  i2s_channel_disable(speaker);
  i2s_del_channel(speaker);
  speaker = nullptr;
  parkSpeakerPins();
  lastPlayedAudio = playedAudio;
  audioEnvelope = 0;
  eyeMode = EyeMode::Idle;
}

bool startSpeaker() {
  if (speaker) return true;
  i2s_chan_config_t channel = I2S_CHANNEL_DEFAULT_CONFIG(I2S_NUM_AUTO, I2S_ROLE_MASTER);
  channel.dma_desc_num = DMA_BUFFERS;
  channel.dma_frame_num = DMA_FRAMES;
  channel.auto_clear_after_cb = true;
  if (i2s_new_channel(&channel, &speaker, nullptr) != ESP_OK) return false;
  const i2s_std_config_t config = {
    .clk_cfg = I2S_STD_CLK_DEFAULT_CONFIG(AUDIO_RATE),
    .slot_cfg = I2S_STD_PHILIPS_SLOT_DEFAULT_CONFIG(I2S_DATA_BIT_WIDTH_16BIT, I2S_SLOT_MODE_STEREO),
    .gpio_cfg = {
      .mclk = I2S_GPIO_UNUSED,
      .bclk = static_cast<gpio_num_t>(AMP_BCLK),
      .ws = static_cast<gpio_num_t>(AMP_LRCLK),
      .dout = static_cast<gpio_num_t>(AMP_DIN),
      .din = I2S_GPIO_UNUSED,
      .invert_flags = {},
    },
  };
  i2s_event_callbacks_t callbacks = {};
  callbacks.on_sent = audioSent;
  if (i2s_channel_init_std_mode(speaker, &config) != ESP_OK
      || i2s_channel_register_event_callback(speaker, &callbacks, nullptr) != ESP_OK
      || i2s_channel_enable(speaker) != ESP_OK) {
    i2s_del_channel(speaker);
    speaker = nullptr;
    parkSpeakerPins();
    return false;
  }
  voiceBrightness = 0;
  eyeMode = EyeMode::Voice;
  return true;
}

bool playPCM() {
  const size_t frames = pcmLength / 3 * 2;
  for (size_t offset = 0; offset < frames; offset += DMA_FRAMES) {
    int16_t mono[DMA_FRAMES];
    const size_t count = min(DMA_FRAMES, frames - offset);
    for (size_t i = 0; i < count; ++i) {
      const size_t frame = offset + i;
      const size_t index = (frame / 2) * 3;
      // Two signed samples share three bytes, with their upper nibbles in byte 2.
      const uint16_t raw12 = (frame % 2 == 0)
        ? pcm[index] | ((uint16_t(pcm[index + 2]) & 0x0f) << 8)
        : pcm[index + 1] | ((uint16_t(pcm[index + 2]) & 0xf0) << 4);
      mono[i] = static_cast<int16_t>(raw12 << 4);
    }
    if (!writeSpeakerBlock(mono, count)) return false;
  }
  return true;
}

bool writeSpeakerBlock(const int16_t *mono, size_t frames) {
  if (frames > DMA_FRAMES || !startSpeaker()) return false;
  int16_t stereo[2 * DMA_FRAMES] = {};
  for (size_t i = 0; i < frames; ++i) {
    const auto sample = static_cast<int16_t>(static_cast<int32_t>(mono[i]) * speakerGain / 100);
    stereo[2 * i] = stereo[2 * i + 1] = sample;
  }
  size_t written = 0;
  if (i2s_channel_write(speaker, stereo, sizeof(stereo), &written, 200) != ESP_OK || written != sizeof(stereo)) {
    stopSpeaker();
    return false;
  }
  updateSpeakerEye();
  ledcWrite(EYE_PIN, voiceBrightness);
  return true;
}

void drainSpeaker() {
  if (!speaker) return;
  const uint32_t start = millis();
  while (millis() - start < (DMA_BUFFERS + 1) * DMA_FRAMES * 1000 / AUDIO_RATE) {
    updateSpeakerEye();
    ledcWrite(EYE_PIN, voiceBrightness);
    delay(1);
  }
  stopSpeaker();
}

bool playTestTone() {
  I2SClass audio;
  audio.setPins(AMP_BCLK, AMP_LRCLK, AMP_DIN);
  if (!audio.begin(I2S_MODE_STD, AUDIO_RATE, I2S_DATA_BIT_WIDTH_16BIT, I2S_SLOT_MODE_STEREO)) {
    return false;
  }

  constexpr size_t blockFrames = 240;
  constexpr size_t toneFrames = 24 * blockFrames;
  constexpr size_t fadeFrames = blockFrames;
  int16_t samples[2 * blockFrames];
  // Repeat the mono tone in both slots so the breakout can use either channel.
  for (size_t offset = 0; offset < toneFrames + 3 * blockFrames; offset += blockFrames) {
    for (size_t i = 0; i < blockFrames; ++i) {
      const size_t frame = offset + i;
      const size_t fade = frame < toneFrames ? min(fadeFrames, min(frame, toneFrames - frame)) : 0;
      const float phase = TWO_PI * (frame % 50) / 50; // 480 Hz at 24 kHz.
      const int16_t sample = static_cast<int16_t>(1200.0f * fade / fadeFrames * sinf(phase));
      samples[2 * i] = sample;
      samples[2 * i + 1] = sample;
    }
    if (audio.write(samples, sizeof(samples)) != sizeof(samples)) {
      audio.end();
      return false;
    }
  }
  // Give the queued tail time to reach the amp before stopping I2S.
  delay(100);
  return audio.end();
}

void stopMic() {
  if (!micActive) return;
  micActive = false;
  mic.end();
}

bool readMicBlock(int16_t *mono) {
  int32_t stereo[2 * MIC_BLOCK_FRAMES];
  if (!micActive || mic.readBytes(reinterpret_cast<char *>(stereo), sizeof(stereo)) != sizeof(stereo)) {
    stopMic();
    return false;
  }
  // L/R is wired low. The high 16 bits of the left slot are signed PCM.
  if (mono) {
    for (size_t i = 0; i < MIC_BLOCK_FRAMES; ++i) mono[i] = stereo[2 * i] >> 16;
  }
  lastMicReadAt = millis();
  return true;
}

bool startMic() {
  stopMic();
  stopSpeaker();
  mic.setPins(MIC_SCK, MIC_WS, -1, MIC_SD);
  mic.setTimeout(200);
  if (!mic.begin(I2S_MODE_STD, MIC_RATE, I2S_DATA_BIT_WIDTH_32BIT, I2S_SLOT_MODE_STEREO)) {
    return false;
  }

  micActive = true;
  // The INMP441 needs startup clocks before its samples are valid.
  for (size_t i = 0; i < 16; ++i) {
    if (!readMicBlock(nullptr)) return false;
  }
  return true;
}

bool captureMicTest() {
  constexpr size_t captureFrames = 3 * MIC_RATE;
  std::unique_ptr<int16_t[]> recording(new (std::nothrow) int16_t[captureFrames]);
  if (!recording || !startMic()) return false;
  for (size_t offset = 0; offset < captureFrames; offset += MIC_BLOCK_FRAMES) {
    if (!readMicBlock(recording.get() + offset)) return false;
  }
  stopMic();
  Serial.printf("MIC PCM16 %lu %lu\n", static_cast<unsigned long>(MIC_RATE), static_cast<unsigned long>(captureFrames));
  Serial.write(reinterpret_cast<const uint8_t *>(recording.get()), captureFrames * sizeof(int16_t));
  return true;
}

void handleCommand() {
  command.trim();
  command.toUpperCase();
  if (handleStandaloneCommand(command)) return;
  if (command == "MIC START" || command == "MIC TEST" || command == "TONE" || command.startsWith("PCM12 ")) {
    suspendStandaloneMic();
  }
  if (command == "HELLO") {
    Serial.println("EMBER PCM12");
  } else if (command == "GAIN") {
    Serial.printf("GAIN %u\n", static_cast<unsigned>(speakerGain));
  } else if (command.startsWith("GAIN ")) {
    const char *value = command.c_str() + 5;
    char *end = nullptr;
    const long gain = strtol(value, &end, 10);
    if (!speaker && end != value && *end == '\0' && gain >= 1 && gain <= 100) {
      speakerGain = static_cast<uint8_t>(gain);
      Serial.printf("GAIN %u\n", static_cast<unsigned>(speakerGain));
    } else {
      Serial.println("ERROR");
    }
  } else if (command == "MIC PROBE") {
    Serial.println("MIC PCM16 16000");
  } else if (command == "MIC START") {
    Serial.println(startMic() ? "OK" : "ERROR");
  } else if (command == "MIC READ") {
    int16_t samples[2 * MIC_BLOCK_FRAMES];
    if (readMicBlock(samples) && readMicBlock(samples + MIC_BLOCK_FRAMES)) {
      Serial.println("MIC DATA 1280");
      Serial.write(reinterpret_cast<const uint8_t *>(samples), sizeof(samples));
    } else {
      Serial.println("ERROR");
    }
  } else if (command == "MIC STOP") {
    if (!standaloneOwnsMic()) stopMic();
    Serial.println("OK");
  } else if (command.startsWith("PCM12 ")) {
    stopMic();
    const char *value = command.c_str() + 6;
    char *end = nullptr;
    const long length = strtol(value, &end, 10);
    if (end != value && *end == '\0' && length > 0 && length <= PCM_MAX_BYTES && length % 3 == 0) {
      pcmLength = length;
      pcmReceived = 0;
      lastPCMAt = millis();
    } else {
      Serial.println("ERROR");
    }
  } else if (command == "END" || command == "STOP") {
    if (command == "END") drainSpeaker();
    else stopSpeaker();
    Serial.println("OK");
  } else if (command == "PULSE" || command == "SPEAK") {
    eyeMode = EyeMode::Pulse;
    pulseStartedAt = millis();
    Serial.println(R"({"event":"response_started","detail":"usb_serial"})");
  } else if (command == "EYES ON") {
    eyeMode = EyeMode::On;
    Serial.println(R"({"event":"eyes","detail":"on"})");
  } else if (command == "EYES OFF") {
    eyeMode = EyeMode::Off;
    Serial.println(R"({"event":"eyes","detail":"off"})");
  } else if (command.startsWith("VOICE ")) {
    const char *value = command.c_str() + 6;
    char *end = nullptr;
    long level = strtol(value, &end, 10);
    if (end != value && *end == '\0' && level >= 0 && level <= 255) {
      if (eyeMode != EyeMode::Voice) {
        Serial.println(R"({"event":"voice_started"})");
      }
      voiceBrightness = static_cast<uint8_t>(level);
      lastVoiceAt = millis();
      eyeMode = EyeMode::Voice;
    }
  } else if (command == "IDLE") {
    eyeMode = EyeMode::Idle;
    Serial.println(R"({"event":"eyes","detail":"idle"})");
  } else if (command == "TONE") {
    stopMic();
    stopSpeaker();
    const bool played = playTestTone();
    parkSpeakerPins();
    Serial.println(played ? R"({"event":"tone_finished"})" : R"({"event":"tone_error"})");
  } else if (command == "MIC TEST") {
    stopSpeaker();
    if (!captureMicTest()) Serial.println("ERROR");
  } else if (command == "STATUS") {
    Serial.println(eyeMode == EyeMode::Voice ? R"({"event":"status","detail":"speaking"})"
                   : eyeMode == EyeMode::Pulse ? R"({"event":"status","detail":"responding"})"
                                              : R"({"event":"status","detail":"idle"})");
  } else if (command.length()) {
    Serial.println(R"({"event":"unknown_command"})");
  }
}

void updateEye(uint32_t now) {
  if (eyeMode == EyeMode::Pulse && now - pulseStartedAt >= PULSE_MS) {
    eyeMode = EyeMode::Idle;
    Serial.println(R"({"event":"response_finished"})");
  }
  if (eyeMode == EyeMode::Voice && !speaker && now - lastVoiceAt >= VOICE_TIMEOUT_MS) {
    eyeMode = EyeMode::Idle;
    Serial.println(R"({"event":"voice_timeout"})");
  }

  uint8_t brightness = 0;
  switch (eyeMode) {
    case EyeMode::Pulse:
      brightness = ((now - pulseStartedAt) / 80) % 2 ? 160 : 255;
      break;
    case EyeMode::Voice:
      brightness = voiceBrightness;
      break;
    case EyeMode::On:
      brightness = 255;
      break;
    case EyeMode::Off:
      break;
    case EyeMode::Idle: {
      const uint32_t phase = now % 3000;
      const uint32_t rise = phase < 1500 ? phase : 3000 - phase;
      brightness = 18 + rise * 70 / 1500;
      break;
    }
  }
  ledcWrite(EYE_PIN, brightness);
}

void setup() {
  Serial.setRxBufferSize(8192);
  Serial.begin(USB_BAUD);
  parkSpeakerPins();
  ledcAttach(EYE_PIN, 5000, 8);
  setupStandalone();
  Serial.println(R"({"event":"boot","detail":"686_bench"})");
  Serial.println("Commands: STATUS, PULSE, EYES ON, EYES OFF, VOICE 0-255, IDLE, TONE, MIC TEST");
}

void loop() {
  while (Serial.available()) {
    if (pcmLength) {
      pcm[pcmReceived++] = static_cast<uint8_t>(Serial.read());
      lastPCMAt = millis();
      if (pcmReceived == pcmLength) {
        Serial.println(playPCM() ? "OK" : "ERROR");
        pcmLength = 0;
        lastPCMAt = millis();
      }
      continue;
    }
    const char incoming = static_cast<char>(Serial.read());
    if (incoming == '\n' || incoming == '\r') {
      handleCommand();
      command = "";
    } else if (command.length() < 80) {
      command += incoming;
    }
  }
  const uint32_t now = millis();
  if (pcmLength && now - lastPCMAt > 500) {
    pcmLength = 0;
    stopSpeaker();
    Serial.println("ERROR");
  }
  if (speaker && now - lastPCMAt > 1500) stopSpeaker();
  if (micActive && !standaloneOwnsMic() && now - lastMicReadAt > 1000) stopMic();
  if (speaker) updateSpeakerEye();
  const bool usbAudio = speaker || (micActive && !standaloneOwnsMic()) || pcmLength;
  const bool usbEye = eyeMode != EyeMode::Idle;
  if (usbAudio || usbEye || !standaloneOwnsEye()) updateEye(now);
  updateStandalone(usbAudio || eyeMode != EyeMode::Idle);
  delay(1);
}
