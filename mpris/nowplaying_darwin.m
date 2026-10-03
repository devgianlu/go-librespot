#import <AppKit/AppKit.h>
#import <MediaPlayer/MediaPlayer.h>

#include <stdatomic.h>

#include "nowplaying_darwin.h"
#include "_cgo_export.h"

// Mirrors nowPlayingCommand and nowPlayingState in nowplaying.go.
enum { CmdTogglePlayPause, CmdPlay, CmdPause, CmdNext, CmdPrevious, CmdStop, CmdSeek };
enum { StateStopped, StatePlaying, StatePaused };

// Touched on the main queue only.
static NSString *currentKey;
static MPMediaItemArtwork *currentArtwork;
static int currentState;

static void addCommand(MPRemoteCommand *command, int kind) {
	command.enabled = YES;
	[command addTargetWithHandler:^MPRemoteCommandHandlerStatus(MPRemoteCommandEvent *event) {
		goNowPlayingCommand(kind, 0);
		return MPRemoteCommandHandlerStatusSuccess;
	}];
}

void nowPlayingSetup(void) {
	// Called on Go threads, which have no autorelease pool of their own.
	@autoreleasepool {
		dispatch_async(dispatch_get_main_queue(), ^{
			MPRemoteCommandCenter *center = [MPRemoteCommandCenter sharedCommandCenter];
			addCommand(center.togglePlayPauseCommand, CmdTogglePlayPause);
			addCommand(center.playCommand, CmdPlay);
			addCommand(center.pauseCommand, CmdPause);
			addCommand(center.nextTrackCommand, CmdNext);
			addCommand(center.previousTrackCommand, CmdPrevious);
			addCommand(center.stopCommand, CmdStop);

			center.changePlaybackPositionCommand.enabled = YES;
			[center.changePlaybackPositionCommand addTargetWithHandler:^MPRemoteCommandHandlerStatus(MPRemoteCommandEvent *event) {
				goNowPlayingCommand(CmdSeek, ((MPChangePlaybackPositionCommandEvent *)event).positionTime);
				return MPRemoteCommandHandlerStatusSuccess;
			}];
		});
	}
}

static MPNowPlayingPlaybackState playbackState(int state) {
	switch (state) {
	case StatePlaying:
		return MPNowPlayingPlaybackStatePlaying;
	case StatePaused:
		return MPNowPlayingPlaybackStatePaused;
	}
	return MPNowPlayingPlaybackStateStopped;
}

void nowPlayingUpdate(const char *key, const char *title, const char *artist, const char *album,
                      double duration, double position, int state) {
	// Called on Go threads, which have no autorelease pool of their own.
	@autoreleasepool {
		NSString *k = [NSString stringWithUTF8String:key];
		NSString *t = [NSString stringWithUTF8String:title];
		NSString *a = [NSString stringWithUTF8String:artist];
		NSString *al = [NSString stringWithUTF8String:album];
		dispatch_async(dispatch_get_main_queue(), ^{
			MPNowPlayingInfoCenter *center = [MPNowPlayingInfoCenter defaultCenter];
			if (![k isEqualToString:currentKey ?: @""]) {
				currentKey = k;
				currentArtwork = nil;
			}
			currentState = state;
			if (state == StateStopped) {
				center.nowPlayingInfo = nil;
				center.playbackState = MPNowPlayingPlaybackStateStopped;
				return;
			}

			NSMutableDictionary *info = [@{
				MPMediaItemPropertyTitle: t,
				MPMediaItemPropertyArtist: a,
				MPMediaItemPropertyAlbumTitle: al,
				MPMediaItemPropertyPlaybackDuration: @(duration),
				MPNowPlayingInfoPropertyElapsedPlaybackTime: @(position),
				MPNowPlayingInfoPropertyPlaybackRate: @(state == StatePlaying ? 1.0 : 0.0),
			} mutableCopy];
			if (currentArtwork != nil) {
				info[MPMediaItemPropertyArtwork] = currentArtwork;
			}
			center.nowPlayingInfo = info;
			center.playbackState = playbackState(state);
		});
	}
}

void nowPlayingSetArtwork(const char *key, const void *data, int length) {
	// Called on Go threads, which have no autorelease pool of their own.
	@autoreleasepool {
		NSString *k = [NSString stringWithUTF8String:key];
		NSData *bytes = [NSData dataWithBytes:data length:length];
		dispatch_async(dispatch_get_main_queue(), ^{
			// Artwork that arrives after the track changed belongs to no one.
			if (![k isEqualToString:currentKey]) {
				return;
			}
			NSImage *image = [[NSImage alloc] initWithData:bytes];
			if (image == nil) {
				return;
			}
			currentArtwork = [[MPMediaItemArtwork alloc] initWithBoundsSize:image.size
			                                                 requestHandler:^NSImage *(CGSize size) { return image; }];

			MPNowPlayingInfoCenter *center = [MPNowPlayingInfoCenter defaultCenter];
			if (center.nowPlayingInfo == nil) {
				return;
			}
			NSMutableDictionary *info = [center.nowPlayingInfo mutableCopy];
			info[MPMediaItemPropertyArtwork] = currentArtwork;
			center.nowPlayingInfo = info;
		});
	}
}

void nowPlayingSetPosition(double position) {
	// Called on Go threads, which have no autorelease pool of their own.
	@autoreleasepool {
		dispatch_async(dispatch_get_main_queue(), ^{
			MPNowPlayingInfoCenter *center = [MPNowPlayingInfoCenter defaultCenter];
			if (center.nowPlayingInfo == nil) {
				return;
			}
			NSMutableDictionary *info = [center.nowPlayingInfo mutableCopy];
			info[MPNowPlayingInfoPropertyElapsedPlaybackTime] = @(position);
			info[MPNowPlayingInfoPropertyPlaybackRate] = @(currentState == StatePlaying ? 1.0 : 0.0);
			center.nowPlayingInfo = info;
		});
	}
}

// Set once the event loop is to end; read by the main thread before every wait.
static atomic_bool stopRequested;

void nowPlayingRunLoop(void) {
	@autoreleasepool {
		[NSApplication sharedApplication];
		// A background agent: no Dock icon, no menu bar.
		[NSApp setActivationPolicy:NSApplicationActivationPolicyProhibited];
		[NSApp finishLaunching];
	}
	// Like -[NSApp run], but checking stopRequested before every wait: -stop:
	// is lost when it arrives before -run has entered its loop.
	while (!atomic_load(&stopRequested)) {
		@autoreleasepool {
			NSEvent *event = [NSApp nextEventMatchingMask:NSEventMaskAny
			                                    untilDate:[NSDate distantFuture]
			                                       inMode:NSDefaultRunLoopMode
			                                      dequeue:YES];
			if (event != nil) {
				[NSApp sendEvent:event];
			}
		}
	}
}

void nowPlayingStopRunLoop(void) {
	atomic_store(&stopRequested, true);
	// Called on a Go thread, which has no autorelease pool of its own.
	@autoreleasepool {
		// Wake the loop if it waits. Run on the main queue, this happens only
		// once the loop runs, and NSApp exists.
		dispatch_async(dispatch_get_main_queue(), ^{
			NSEvent *wake = [NSEvent otherEventWithType:NSEventTypeApplicationDefined
			                                   location:NSZeroPoint
			                              modifierFlags:0
			                                  timestamp:0
			                               windowNumber:0
			                                    context:nil
			                                    subtype:0
			                                      data1:0
			                                      data2:0];
			[NSApp postEvent:wake atStart:YES];
		});
	}
}
