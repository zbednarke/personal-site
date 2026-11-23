#pragma once

#include <algorithm>
#include <cstdint>

namespace ember {

// IMA ADPCM packs two 8 kHz samples per byte. Each turn starts with a
// zeroed predictor. The wire protocol still receives 16 kHz PCM16.
class VoiceAdpcm {
 public:
  uint8_t encode(int sample) {
    const int step = steps[index];
    const int error = sample - predictor;
    const int magnitude = error < 0 ? -error : error;
    uint8_t code = error < 0 ? 8 : 0;
    int remainder = magnitude;
    if (remainder >= step) {
      code |= 4;
      remainder -= step;
    }
    if (remainder >= step / 2) {
      code |= 2;
      remainder -= step / 2;
    }
    if (remainder >= step / 4) code |= 1;
    decode(code);
    return code;
  }

  int16_t decode(uint8_t code) {
    const int step = steps[index];
    int delta = step / 8;
    if (code & 4) delta += step;
    if (code & 2) delta += step / 2;
    if (code & 1) delta += step / 4;
    predictor = std::max(-32768, std::min(32767, predictor + (code & 8 ? -delta : delta)));
    index = std::max(0, std::min(88, index + indexChange[code & 7]));
    return static_cast<int16_t>(predictor);
  }

 private:
  int predictor = 0;
  int index = 0;
  static constexpr int indexChange[] = {-1, -1, -1, -1, 2, 4, 6, 8};
  static constexpr int steps[] = {
      7, 8, 9, 10, 11, 12, 13, 14, 16, 17, 19, 21, 23, 25, 28, 31, 34, 37,
      41, 45, 50, 55, 60, 66, 73, 80, 88, 97, 107, 118, 130, 143, 157, 173,
      190, 209, 230, 253, 279, 307, 337, 371, 408, 449, 494, 544, 598, 658,
      724, 796, 876, 963, 1060, 1166, 1282, 1411, 1552, 1707, 1878, 2066,
      2272, 2499, 2749, 3024, 3327, 3660, 4026, 4428, 4871, 5358, 5894,
      6484, 7132, 7845, 8630, 9493, 10442, 11487, 12635, 13899, 15289,
      16818, 18500, 20350, 22385, 24623, 27086, 29794, 32767};
};

}
