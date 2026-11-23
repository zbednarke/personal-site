#include "wake_question.h"
#include <cassert>

int main() {
  using Q = ember::WakeQuestion;
  using E = Q::Event;
  ember::FollowupVad idle;
  for (int i = 0; i < 80; ++i) idle.observeIdle(5);
  ember::WakeQuestionAmbient ambient;
  for (int i = 0; i < 80; ++i) ambient.observe(5);
  for (int i = 0; i < 24; ++i) ambient.observe(90);
  Q quietAfterLoudWake(ambient.snapshot());
  for (unsigned i = 0; i < 5; ++i) assert(quietAfterLoudWake.feed(15, i) == E::Continue);
  assert(quietAfterLoudWake.feed(15, 5) == E::Started);

  Q delayed(idle);
  for (unsigned i = 0; i < 210; ++i) assert(delayed.feed(5, i) == E::Continue);
  for (unsigned i = 210; i < 215; ++i) assert(delayed.feed(30, i) == E::Continue);
  assert(delayed.feed(30, 215) == E::Started);
  assert(delayed.hasSpeech());
  assert(delayed.onsetBlock() == 213 && delayed.firstAudioBlock() == 197);
  for (unsigned i = 216; i < 270; ++i) assert(delayed.feed(5, i) == E::Continue);
  assert(delayed.feed(25, 270) == E::Continue);
  for (unsigned i = 271; i < 330; ++i) assert(delayed.feed(5, i) == E::Continue);
  assert(delayed.feed(5, 330) == E::Finished);

  Q silent(idle);
  for (unsigned i = 0; i < Q::waitBlocks-1; ++i) {
    assert(silent.feed(i > 20 && i <= 24 ? 30 : 5, i) == E::Continue);
  }
  assert(silent.feed(5, Q::waitBlocks-1) == E::NoSpeech);

  Q lastSecond(idle);
  for (unsigned i = 0; i < Q::waitBlocks-4; ++i) assert(lastSecond.feed(5, i) == E::Continue);
  for (unsigned i = Q::waitBlocks-4; i <= Q::waitBlocks; ++i) assert(lastSecond.feed(30, i) == E::Continue);
  assert(lastSecond.feed(30, Q::waitBlocks+1) == E::Started);
}
