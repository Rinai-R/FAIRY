#import <CoreGraphics/CoreGraphics.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#include <stdatomic.h>

int fairy_preflight_screen_capture(void) {
    return CGPreflightScreenCaptureAccess() ? 1 : 0;
}

static int fairy_copy_image(CGImageRef source, unsigned char **pixels, size_t *width, size_t *height, size_t *stride) {
    size_t w = CGImageGetWidth(source);
    size_t h = CGImageGetHeight(source);
    if (w == 0 || h == 0 || w > 16384 || h > 16384 || w > SIZE_MAX / h || w * h > 67108864) {
        return 2;
    }
    size_t row = w * 4;
    unsigned char *buffer = (unsigned char *)calloc(h, row);
    if (buffer == NULL) {
        return 3;
    }
    CGColorSpaceRef color = CGColorSpaceCreateDeviceRGB();
    CGContextRef context = CGBitmapContextCreate(buffer, w, h, 8, row, color,
        kCGImageAlphaPremultipliedLast | kCGBitmapByteOrder32Big);
    CGColorSpaceRelease(color);
    if (context == NULL) {
        free(buffer);
        return 4;
    }
    CGContextDrawImage(context, CGRectMake(0, 0, w, h), source);
    CGContextRelease(context);
    *pixels = buffer;
    *width = w;
    *height = h;
    *stride = row;
    return 0;
}

int fairy_capture_main_display(unsigned char **pixels, size_t *width, size_t *height, size_t *stride, long long timeout_ms) {
    if (@available(macOS 14.0, *)) {
        @autoreleasepool {
            dispatch_semaphore_t done = dispatch_semaphore_create(0);
            __block CGImageRef captured = NULL;
            __block int result = 1;
			__block atomic_bool accepting;
			atomic_init(&accepting, true);
            [SCShareableContent getShareableContentExcludingDesktopWindows:NO
                onScreenWindowsOnly:YES
                completionHandler:^(SCShareableContent *content, NSError *error) {
					if (!atomic_load(&accepting) || error != nil || content == nil) {
                        dispatch_semaphore_signal(done);
                        return;
                    }
                    SCDisplay *mainDisplay = nil;
                    CGDirectDisplayID displayID = CGMainDisplayID();
                    for (SCDisplay *display in content.displays) {
                        if (display.displayID == displayID) {
                            mainDisplay = display;
                            break;
                        }
                    }
                    if (mainDisplay == nil) {
                        dispatch_semaphore_signal(done);
                        return;
                    }
                    SCContentFilter *filter = [[SCContentFilter alloc] initWithDisplay:mainDisplay excludingWindows:@[]];
                    SCStreamConfiguration *configuration = [[SCStreamConfiguration alloc] init];
                    configuration.width = mainDisplay.width;
                    configuration.height = mainDisplay.height;
                    configuration.showsCursor = NO;
                    configuration.capturesAudio = NO;
                    [SCScreenshotManager captureImageWithFilter:filter configuration:configuration completionHandler:^(CGImageRef image, NSError *captureError) {
						if (atomic_load(&accepting) && captureError == nil && image != NULL) {
                            captured = CGImageRetain(image);
                            result = 0;
                        }
                        dispatch_semaphore_signal(done);
                    }];
                }];
            dispatch_time_t deadline = dispatch_time(DISPATCH_TIME_NOW, timeout_ms * NSEC_PER_MSEC);
            if (dispatch_semaphore_wait(done, deadline) != 0) {
				atomic_store(&accepting, false);
                return 6;
            }
			atomic_store(&accepting, false);
            if (result != 0 || captured == NULL) {
                return result;
            }
            result = fairy_copy_image(captured, pixels, width, height, stride);
            CGImageRelease(captured);
            return result;
        }
    }
    return 5;
}
