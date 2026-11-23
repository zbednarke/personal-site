#include "wake_word.h"

#include <algorithm>
#include <cmath>

namespace wake {
namespace {
constexpr float pi = 3.14159265f;
constexpr size_t samples = 160;
constexpr size_t fftSize = 256;
constexpr size_t bandCount = 22;
constexpr uint16_t unreachable = 65535;

float mel(float hz) {
  return log1pf(hz / 700.0f);
}

struct Analysis {
  float window[samples];
  float cosine[fftSize / 2];
  float sine[fftSize / 2];
  float dct[coefficients][bandCount];
  unsigned edge[bandCount + 2];

  Analysis() {
    for (size_t i = 0; i < samples; ++i) window[i] = 0.54f - 0.46f * cosf(2 * pi * i / (samples - 1));
    for (size_t i = 0; i < fftSize / 2; ++i) {
      cosine[i] = cosf(2 * pi * i / fftSize);
      sine[i] = -sinf(2 * pi * i / fftSize);
    }
    for (size_t i = 0; i < bandCount + 2; ++i) {
      const float point = mel(125) + (mel(3800) - mel(125)) * i / (bandCount + 1);
      edge[i] = lroundf(700 * expm1f(point) * fftSize / 8000);
    }
    for (size_t c = 0; c < coefficients; ++c) {
      for (size_t band = 0; band < bandCount; ++band) {
        dct[c][band] = cosf(pi * (c + 1) * (band + 0.5f) / bandCount);
      }
    }
  }
};
}

unsigned level(const int16_t *pcm) {
  uint32_t sum = 0;
  for (size_t i = 1; i < 2 * samples; ++i) sum += abs(int(pcm[i]) - pcm[i - 1]);
  return sum / (2 * samples - 1);
}

Frame features(const int16_t *pcm) {
  static const Analysis table;
  float real[fftSize] = {};
  float imaginary[fftSize] = {};
  unsigned reversed = 0;
  float previous = 0;
  for (size_t i = 0; i < fftSize; ++i) {
    if (i < samples) {
      // Low-pass before decimation and de-emphasize very low frequencies.
      const float sample = (float(pcm[2 * i]) + pcm[2 * i + 1]) * 0.5f;
      real[reversed] = (sample - 0.95f * previous) * table.window[i];
      previous = sample;
    }
    unsigned bit = fftSize >> 1;
    while (bit && (reversed & bit)) {
      reversed ^= bit;
      bit >>= 1;
    }
    reversed ^= bit;
  }
  for (size_t width = 2; width <= fftSize; width *= 2) {
    for (size_t base = 0; base < fftSize; base += width) {
      for (size_t i = 0; i < width / 2; ++i) {
        const size_t angle = i * fftSize / width;
        const size_t left = base + i;
        const size_t right = left + width / 2;
        const float r = real[right] * table.cosine[angle] - imaginary[right] * table.sine[angle];
        const float q = imaginary[right] * table.cosine[angle] + real[right] * table.sine[angle];
        real[right] = real[left] - r;
        imaginary[right] = imaginary[left] - q;
        real[left] += r;
        imaginary[left] += q;
      }
    }
  }
  for (size_t i = 0; i < fftSize / 2; ++i) real[i] = real[i] * real[i] + imaginary[i] * imaginary[i];
  float bands[bandCount];
  for (size_t band = 0; band < bandCount; ++band) {
    const unsigned low = table.edge[band];
    const unsigned mid = table.edge[band + 1];
    const unsigned high = table.edge[band + 2];
    float power = real[mid];
    for (unsigned bin = low + 1; bin < mid; ++bin) power += real[bin] * (bin - low) / (mid - low);
    for (unsigned bin = mid + 1; bin < high; ++bin) power += real[bin] * (high - bin) / (high - mid);
    bands[band] = logf(power + 4096);
  }
  Frame result;
  for (size_t c = 0; c < coefficients; ++c) {
    float value = 0;
    for (size_t band = 0; band < bandCount; ++band) value += table.dct[c][band] * bands[band];
    result.value[c] = std::max(-127l, std::min(127l, lroundf(value * 1.5f)));
  }
  return result;
}

void Detector::reset() {
  for (auto &path : paths) {
    for (auto &alignment : path) alignment = {unreachable, 0, 0, 0, 0};
  }
}

bool Detector::feed(const Model &model, const Frame &frame, bool speech, unsigned sensitivity, Match *closest) {
  for (size_t i = 0; i < exampleCount; ++i) {
    const auto &example = model.examples[i];
    if (example.length < minFrames || example.length > maxFrames) continue;
    auto &path = paths[i];
    // Each input block advances the alignment by zero, one, or two reference
    // blocks. Score a sliding suffix so the phrase can occur in continuous audio.
    path[0] = {};
    for (size_t j = example.length; j > 0; --j) {
      const auto distance = [&](size_t index) {
        unsigned cost = 0;
        const auto &reference = example.frames[index];
        for (size_t c = 0; c < coefficients; ++c) cost += abs(int(frame.value[c]) - reference.value[c]);
        return cost;
      };
      const unsigned frameCost = distance(j - 1);
      const size_t section = (j - 1) * 4 / example.length;
      Alignment best{unreachable, 0, 0, 0, 0};
      const auto consider = [&](size_t previous, unsigned penalty) {
        Alignment candidate = path[previous];
        if (candidate.cost == unreachable || (previous == 0 && !speech)
            || candidate.age >= example.length * 8 / 5
            || candidate.age + 6 < j * 3 / 5 || candidate.age > j * 8 / 5 + 5) return;
        const unsigned cost = candidate.cost + penalty + frameCost;
        if (cost >= best.cost) return;
        if (previous && (previous - 1) * 4 / example.length != section) {
          const auto mean = candidate.segmentCost / candidate.segmentAge;
          candidate.worstSection = std::max(candidate.worstSection, uint8_t(std::min(mean, 255)));
          candidate.segmentAge = 0;
          candidate.segmentCost = 0;
        }
        best = {uint16_t(cost), uint8_t(candidate.age + 1), uint8_t(candidate.segmentAge + 1),
                candidate.worstSection, uint16_t(candidate.segmentCost + frameCost + penalty / 3)};
      };
      consider(j - 1, 0);
      consider(j, 55);
      if (j > 2) consider(j - 2, 50 + distance(j - 2) / 2);
      path[j] = best;
    }
    const auto end = path[example.length];
    if (end.age < example.length * 7 / 10) continue;
    const unsigned mean = end.cost / end.age;
    const unsigned worst = std::max(unsigned(end.worstSection), unsigned(end.segmentCost / end.segmentAge));
    if (closest && mean + worst < closest->mean + closest->worstSection) {
      *closest = {mean, worst};
    }
    if (mean <= sensitivity && worst <= sensitivity + 17) return true;
  }
  return false;
}
}
