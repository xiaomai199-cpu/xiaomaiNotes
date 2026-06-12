//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

@interface MarkNotesAppDelegate : NSObject <NSApplicationDelegate>
@property (strong) NSWindow *window;
@property (strong) NSString *url;
@end

@implementation MarkNotesAppDelegate

- (void)applicationDidFinishLaunching:(NSNotification *)note {
    NSRect frame = NSMakeRect(0, 0, 1280, 840);
    NSUInteger style = NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
                       NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable;
    self.window = [[NSWindow alloc] initWithContentRect:frame
                                              styleMask:style
                                                backing:NSBackingStoreBuffered
                                                  defer:NO];
    [self.window setTitle:@"MarkNotes"];
    [self.window center];
    [self.window setFrameAutosaveName:@"MarkNotesMainWindow"];

    WKWebViewConfiguration *cfg = [[WKWebViewConfiguration alloc] init];
    WKWebView *web = [[WKWebView alloc] initWithFrame:frame configuration:cfg];
    [web setAutoresizingMask:NSViewWidthSizable | NSViewHeightSizable];
    [self.window setContentView:web];
    [web loadRequest:[NSURLRequest requestWithURL:[NSURL URLWithString:self.url]]];

    [self.window makeKeyAndOrderFront:nil];
    [NSApp activateIgnoringOtherApps:YES];
}

- (BOOL)applicationShouldTerminateAfterLastWindowClosed:(NSApplication *)app {
    return YES;
}

- (BOOL)applicationShouldHandleReopen:(NSApplication *)app hasVisibleWindows:(BOOL)flag {
    if (!flag) [self.window makeKeyAndOrderFront:nil];
    return YES;
}

@end

// WKWebView 本身不处理 Cmd+C/V 等快捷键，必须挂一个标准的编辑菜单才能让它们生效
static void buildMenu(void) {
    NSMenu *menubar = [[NSMenu alloc] init];

    NSMenuItem *appItem = [[NSMenuItem alloc] init];
    [menubar addItem:appItem];
    NSMenu *appMenu = [[NSMenu alloc] init];
    [appMenu addItemWithTitle:@"隐藏 MarkNotes" action:@selector(hide:) keyEquivalent:@"h"];
    [appMenu addItem:[NSMenuItem separatorItem]];
    [appMenu addItemWithTitle:@"退出 MarkNotes" action:@selector(terminate:) keyEquivalent:@"q"];
    [appItem setSubmenu:appMenu];

    NSMenuItem *editItem = [[NSMenuItem alloc] init];
    [menubar addItem:editItem];
    NSMenu *editMenu = [[NSMenu alloc] initWithTitle:@"编辑"];
    [editMenu addItemWithTitle:@"撤销" action:@selector(undo:) keyEquivalent:@"z"];
    [editMenu addItemWithTitle:@"重做" action:@selector(redo:) keyEquivalent:@"Z"];
    [editMenu addItem:[NSMenuItem separatorItem]];
    [editMenu addItemWithTitle:@"剪切" action:@selector(cut:) keyEquivalent:@"x"];
    [editMenu addItemWithTitle:@"拷贝" action:@selector(copy:) keyEquivalent:@"c"];
    [editMenu addItemWithTitle:@"粘贴" action:@selector(paste:) keyEquivalent:@"v"];
    [editMenu addItemWithTitle:@"全选" action:@selector(selectAll:) keyEquivalent:@"a"];
    [editItem setSubmenu:editMenu];

    NSMenuItem *winItem = [[NSMenuItem alloc] init];
    [menubar addItem:winItem];
    NSMenu *winMenu = [[NSMenu alloc] initWithTitle:@"窗口"];
    [winMenu addItemWithTitle:@"最小化" action:@selector(performMiniaturize:) keyEquivalent:@"m"];
    [winMenu addItemWithTitle:@"关闭窗口" action:@selector(performClose:) keyEquivalent:@"w"];
    [winItem setSubmenu:winMenu];

    [NSApp setMainMenu:menubar];
}

static void runWindow(const char *url) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
        buildMenu();
        MarkNotesAppDelegate *delegate = [[MarkNotesAppDelegate alloc] init];
        delegate.url = [NSString stringWithUTF8String:url];
        [NSApp setDelegate:delegate];
        [NSApp run];
    }
}
*/
import "C"

import (
	"runtime"
	"unsafe"
)

// Cocoa 事件循环必须运行在进程主线程上
func init() {
	runtime.LockOSThread()
}

// runNativeWindow 打开指向本地服务的原生窗口，窗口关闭后才返回。
func runNativeWindow(url string) {
	curl := C.CString(url)
	defer C.free(unsafe.Pointer(curl))
	C.runWindow(curl)
}
