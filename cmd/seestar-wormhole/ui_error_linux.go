//go:build gui && cgo && !guitest

package main

/*
#cgo pkg-config: gtk4
#include <gtk/gtk.h>
#include <stdlib.h>

static void startup_error_response(GtkDialog *dialog, int response, gpointer loop) {
 g_main_loop_quit(loop);
}

static void startup_error(const char *title, const char *message) {
 if (!gtk_init_check()) return;
 GtkWidget *dialog = gtk_message_dialog_new(NULL, GTK_DIALOG_MODAL,
     GTK_MESSAGE_ERROR, GTK_BUTTONS_CLOSE, "%s", title);
 gtk_window_set_title(GTK_WINDOW(dialog), title);
 gtk_message_dialog_format_secondary_text(GTK_MESSAGE_DIALOG(dialog), "%s", message);
 GMainLoop *loop = g_main_loop_new(NULL, FALSE);
 g_signal_connect(dialog, "response", G_CALLBACK(startup_error_response), loop);
 gtk_window_present(GTK_WINDOW(dialog));
 g_main_loop_run(loop);
 gtk_window_destroy(GTK_WINDOW(dialog));
 g_main_loop_unref(loop);
}
*/
import "C"
import "unsafe"

func showUIStartupError(err error) {
	title, message := C.CString(uiStartupErrorTitle), C.CString(err.Error())
	defer C.free(unsafe.Pointer(title))
	defer C.free(unsafe.Pointer(message))
	C.startup_error(title, message)
}
