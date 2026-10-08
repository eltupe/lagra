//go:build darwin

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#include <string.h>

static NSWindow *lagraWindow = nil;
static WKWebView *lagraWeb = nil;
static NSURL *lagraLoaded = nil; // the URL last loaded, token included

// Lagra lives in the menu bar and has no main menu, so the standard editing
// shortcuts have no Edit menu items to fire: macOS beeps and nothing is
// pasted (the context menu works because it sends paste: directly). The
// settings window sends those actions itself.
@interface LagraSettingsWindow : NSWindow
@end

@implementation LagraSettingsWindow

- (BOOL)performKeyEquivalent:(NSEvent *)event {
    NSEventModifierFlags flags = event.modifierFlags & NSEventModifierFlagDeviceIndependentFlagsMask & ~NSEventModifierFlagCapsLock;
    BOOL cmd = flags == NSEventModifierFlagCommand;
    BOOL cmdShift = flags == (NSEventModifierFlagCommand | NSEventModifierFlagShift);
    if (event.type == NSEventTypeKeyDown && (cmd || cmdShift)) {
        NSString *key = event.charactersIgnoringModifiers.lowercaseString;
        SEL action = NULL;
        if (cmd && [key isEqualToString:@"x"]) action = @selector(cut:);
        else if (cmd && [key isEqualToString:@"c"]) action = @selector(copy:);
        else if (cmd && [key isEqualToString:@"v"]) action = @selector(paste:);
        else if (cmd && [key isEqualToString:@"a"]) action = @selector(selectAll:);
        else if ([key isEqualToString:@"z"]) action = cmd ? @selector(undo:) : @selector(redo:);
        else if (cmd && [key isEqualToString:@"w"]) action = @selector(performClose:);
        if (action && [NSApp sendAction:action to:nil from:self]) {
            return YES;
        }
    }
    return [super performKeyEquivalent:event];
}

@end

@interface LagraWebDelegate : NSObject <WKNavigationDelegate, WKUIDelegate, NSWindowDelegate>
@end

@implementation LagraWebDelegate

// Links that leave the local settings server open in the default browser.
- (void)webView:(WKWebView *)webView decidePolicyForNavigationAction:(WKNavigationAction *)action
        decisionHandler:(void (^)(WKNavigationActionPolicy))decisionHandler {
    NSURL *url = action.request.URL;
    BOOL local = [url.host isEqualToString:@"127.0.0.1"] || [url.host isEqualToString:@"localhost"];
    if (action.navigationType == WKNavigationTypeLinkActivated && !local) {
        [[NSWorkspace sharedWorkspace] openURL:url];
        decisionHandler(WKNavigationActionPolicyCancel);
        return;
    }
    decisionHandler(WKNavigationActionPolicyAllow);
}

// window.open / target=_blank → default browser.
- (WKWebView *)webView:(WKWebView *)webView createWebViewWithConfiguration:(WKWebViewConfiguration *)configuration
        forNavigationAction:(WKNavigationAction *)action windowFeatures:(WKWindowFeatures *)windowFeatures {
    if (action.request.URL) {
        [[NSWorkspace sharedWorkspace] openURL:action.request.URL];
    }
    return nil;
}

// The page process crashed: reopening would show a blank window otherwise.
- (void)webViewWebContentProcessDidTerminate:(WKWebView *)webView {
    [webView reload];
}

// JavaScript confirm() → native sheet.
- (void)webView:(WKWebView *)webView runJavaScriptConfirmPanelWithMessage:(NSString *)message
        initiatedByFrame:(WKFrameInfo *)frame completionHandler:(void (^)(BOOL))completionHandler {
    NSAlert *alert = [[NSAlert alloc] init];
    alert.messageText = message;
    [alert addButtonWithTitle:@"OK"];
    [alert addButtonWithTitle:@"Cancel"];
    [alert beginSheetModalForWindow:lagraWindow completionHandler:^(NSModalResponse r) {
        completionHandler(r == NSAlertFirstButtonReturn);
    }];
}

- (void)webView:(WKWebView *)webView runJavaScriptAlertPanelWithMessage:(NSString *)message
        initiatedByFrame:(WKFrameInfo *)frame completionHandler:(void (^)(void))completionHandler {
    NSAlert *alert = [[NSAlert alloc] init];
    alert.messageText = message;
    [alert addButtonWithTitle:@"OK"];
    [alert beginSheetModalForWindow:lagraWindow completionHandler:^(NSModalResponse r) { completionHandler(); }];
}

@end

static LagraWebDelegate *lagraDelegate = nil;

void lagra_window_open(const char *curl, const char *ctitle, int width, int height) {
    NSString *urlString = [NSString stringWithUTF8String:curl];
    NSString *title = [NSString stringWithUTF8String:ctitle];
    dispatch_async(dispatch_get_main_queue(), ^{
        if (lagraWindow == nil) {
            lagraDelegate = [[LagraWebDelegate alloc] init];
            NSRect frame = NSMakeRect(0, 0, width, height);
            NSWindowStyleMask style = NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
                                      NSWindowStyleMaskResizable | NSWindowStyleMaskMiniaturizable;
            lagraWindow = [[LagraSettingsWindow alloc] initWithContentRect:frame styleMask:style
                                                       backing:NSBackingStoreBuffered defer:NO];
            lagraWindow.releasedWhenClosed = NO;
            lagraWindow.minSize = NSMakeSize(640, 480);
            lagraWindow.delegate = lagraDelegate;
            [lagraWindow center];
            [lagraWindow setFrameAutosaveName:@"lagraSettings"];

            WKWebViewConfiguration *cfg = [[WKWebViewConfiguration alloc] init];
            lagraWeb = [[WKWebView alloc] initWithFrame:[lagraWindow.contentView bounds] configuration:cfg];
            lagraWeb.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
            lagraWeb.navigationDelegate = lagraDelegate;
            lagraWeb.UIDelegate = lagraDelegate;
            [lagraWindow.contentView addSubview:lagraWeb];
        }
        lagraWindow.title = title;
        // Reopening shows the page as it is: a reload would drop unsaved
        // edits. The URL only changes when the daemon restarted.
        NSURL *url = [NSURL URLWithString:urlString];
        if (url && ![lagraLoaded isEqual:url]) {
            lagraLoaded = url;
            [lagraWeb loadRequest:[NSURLRequest requestWithURL:url]];
        }
        [lagraWindow makeKeyAndOrderFront:nil];
        [NSApp activateIgnoringOtherApps:YES];
    });
}

// Runs a folder chooser on the main thread; returns a malloc'd path or NULL.
char *lagra_pick_directory(const char *cstart) {
    NSString *start = [NSString stringWithUTF8String:cstart];
    __block char *result = NULL;
    void (^pick)(void) = ^{
        NSOpenPanel *panel = [NSOpenPanel openPanel];
        panel.canChooseDirectories = YES;
        panel.canChooseFiles = NO;
        panel.canCreateDirectories = YES;
        panel.allowsMultipleSelection = NO;
        panel.prompt = @"Choose";
        if (start.length > 0) {
            panel.directoryURL = [NSURL fileURLWithPath:start];
        }
        [NSApp activateIgnoringOtherApps:YES];
        if ([panel runModal] == NSModalResponseOK && panel.URL) {
            result = strdup(panel.URL.path.UTF8String);
        }
    };
    if ([NSThread isMainThread]) {
        pick();
    } else {
        dispatch_sync(dispatch_get_main_queue(), pick);
    }
    return result;
}
