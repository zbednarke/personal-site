#pragma once

#include <cstddef>
#include <cstdint>

namespace wake {
constexpr size_t coefficients = 12;
constexpr size_t minFrames = 30;
constexpr size_t maxFrames = 120;
constexpr size_t enrollmentCount = 3;
constexpr size_t exampleCount = 2 * enrollmentCount;

struct Frame {
  int8_t value[coefficients];
};

struct Example {
  uint8_t length;
  Frame frames[maxFrames];
};

struct Model {
  uint32_t version;
  Example examples[exampleCount];
};

unsigned level(const int16_t *pcm);
Frame features(const int16_t *pcm);

struct Match {
  unsigned mean = 999;
  unsigned worstSection = 999;
};

class Detector {
 public:
  void reset();
  bool feed(const Model &model, const Frame &frame, bool speech, unsigned sensitivity, Match *closest = nullptr);

 private:
  struct Alignment {
    uint16_t cost;
    uint8_t age;
    uint8_t segmentAge;
    uint8_t worstSection;
    uint16_t segmentCost;
  };
  Alignment paths[exampleCount][maxFrames + 1] = {};
};
}
