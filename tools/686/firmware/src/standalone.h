#pragma once

#include <Arduino.h>

void setupStandalone();
void updateStandalone(bool audioInUse);
bool handleStandaloneCommand(const String &command);

bool startMic();
bool readMicBlock(int16_t *mono);
void stopMic();
bool writeSpeakerBlock(const int16_t *mono, size_t frames);
void drainSpeaker();
void stopSpeaker();

bool standaloneOwnsEye();
bool standaloneOwnsMic();
void suspendStandaloneMic();
