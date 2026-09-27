#ifndef NOWPLAYING_DARWIN_H
#define NOWPLAYING_DARWIN_H

void nowPlayingSetup(void);
void nowPlayingUpdate(const char *key, const char *title, const char *artist, const char *album,
                      double duration, double position, int state);
void nowPlayingSetArtwork(const char *key, const void *data, int length);
void nowPlayingSetPosition(double position);
void nowPlayingRunLoop(void);
void nowPlayingStopRunLoop(void);

#endif
