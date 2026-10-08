//go:build darwin

#import <Cocoa/Cocoa.h>

extern void lagraQuitEvent(void);

@interface LagraQuitHandler : NSObject
@end

@implementation LagraQuitHandler

// Replies at once; the process ends when the daemon has shut down.
- (void)handleQuit:(NSAppleEventDescriptor *)event withReplyEvent:(NSAppleEventDescriptor *)reply {
    lagraQuitEvent();
}

@end

// NSAppleEventManager does not retain its handlers.
static LagraQuitHandler *lagraQuitHandler = nil;

void lagra_handle_quit_event(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (lagraQuitHandler == nil) {
            lagraQuitHandler = [LagraQuitHandler new];
        }
        [[NSAppleEventManager sharedAppleEventManager] setEventHandler:lagraQuitHandler
                                                          andSelector:@selector(handleQuit:withReplyEvent:)
                                                        forEventClass:kCoreEventClass
                                                           andEventID:kAEQuitApplication];
    });
}
