package cli

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime"

	"github.com/spf13/cobra"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// scanGOOS is the system scan's hint is for; tests replace it.
var scanGOOS = runtime.GOOS

// errScanBlocked is a scan whose sends the system refused, as macOS does
// when Local Network access is off for the program.
var errScanBlocked = errors.New("LAN scan: sending was refused (no route to host)")

type scanOptions struct {
	format string
}

// NewScan returns the scan command of program. It is read-only.
func NewScan(program string) *Command {
	var o scanOptions
	c := &Command{}
	c.Command = &cobra.Command{
		Use:   "scan [--format text|json]",
		Short: "List the Seestars on this network",
		Long: `List the Seestars on this network, as the phone app finds them.
scan sends the phone app's scan request to UDP port 4720 on the broadcast
address of each network this computer is on, three times 2 s apart, and
lists every telescope that answers within 6 s. It skips loopback and
point-to-point (VPN) interfaces, and replies from this computer's own
addresses, such as a proxy here that answers for the phone app.
--format text, the default, writes one line per address on stdout: the
address, busy or - (busy when the telescope reports as many clients as the
phone app allows; copying still works), the name, the model and the serial
number, each quoted in Go string syntax if it holds a space, a quote, a
backslash, a character that is not printable, or bytes that are not UTF-8.
--format json writes one JSON object with the members telescopes (each
with name, model, serial, addresses and busy) and replies, the number of
replies. The log names each telescope found. A telescope found here can
be given to sync, find, browse and delete as --endpoint ADDRESS:445.
On macOS, scan needs Local Network access (System Settings > Privacy &
Security > Local Network). Without it, sending fails and scan says so.
Logs and errors go to stderr.
Exit 0 when the scan ran, even if nothing answered, 1 when it couldn't
send, 2 on a usage error.`,
		Example: "  " + program + " scan\n" +
			"  " + program + " scan --format json",
		Args: noArguments,
		RunE: func(*cobra.Command, []string) error {
			if o.format != "text" && o.format != "json" {
				return &diagnostic.OptionError{Option: "--format", Rule: "use text or json"}
			}
			c.store(true, func(ctx context.Context, out io.Writer, log *slog.Logger) error {
				return runScan(ctx, o, out, log)
			})
			return nil
		},
	}
	c.Flags().StringVar(&o.format, "format", "text", "output format: text or json")
	return c
}

// scanned is a telescope as scan's JSON lists it.
type scanned struct {
	Name      string   `json:"name"`
	Model     string   `json:"model"`
	Serial    string   `json:"serial"`
	Addresses []string `json:"addresses"`
	Busy      bool     `json:"busy"`
}

// runScan scans this network and lists what answered to out. The log
// records the reply and telescope counts, and names each telescope.
func runScan(ctx context.Context, o scanOptions, out io.Writer, log *slog.Logger) error {
	result, err := lanScanner.Scan(ctx)
	if err != nil {
		return diagnostic.At("scan", err)
	}
	if result.Blocked {
		return diagnostic.At("scan", fmt.Errorf("%w%s", errScanBlocked, scanBlockedHint(scanGOOS)))
	}
	log.Info("scanned this network", "replies", result.Replies, "telescopes", len(result.Telescopes))
	if len(result.Telescopes) == 0 {
		log.Warn("no telescope answered" + scanSilentHint(scanGOOS))
	}
	list := struct {
		Telescopes []scanned `json:"telescopes"`
		Replies    int       `json:"replies"`
	}{Telescopes: []scanned{}, Replies: result.Replies}
	for _, t := range result.Telescopes {
		row := scanned{Name: t.Name, Model: t.Model, Serial: t.Serial, Addresses: []string{}, Busy: t.Busy()}
		for _, a := range t.Addresses {
			row.Addresses = append(row.Addresses, a.String())
		}
		list.Telescopes = append(list.Telescopes, row)
		log.Info("found a telescope", "telescope", t.CappedLabel(), "addresses", row.Addresses)
	}
	w := bufio.NewWriter(out)
	if o.format == "json" {
		if err = json.MarshalWrite(w, list, jsontext.EscapeForJS(true), jsontext.AllowInvalidUTF8(true)); err == nil {
			err = w.WriteByte('\n')
		}
	} else {
		for _, t := range list.Telescopes {
			busy := "-"
			if t.Busy {
				busy = "busy"
			}
			for _, a := range t.Addresses {
				if _, err = fmt.Fprintf(w, "%s %s %s %s %s\n", a, busy, quoteField(t.Name), quoteField(t.Model), quoteField(t.Serial)); err != nil {
					break
				}
			}
		}
	}
	if err == nil {
		err = w.Flush()
	}
	if err != nil {
		return fmt.Errorf("write the list: %w", err)
	}
	return nil
}

// quoteField is a name, model or serial as a text line shows it: quoted
// as a path is, and "" when empty, so a line always has five fields.
func quoteField(s string) string {
	if s == "" {
		return `""`
	}
	return quotePath(s)
}

func scanBlockedHint(goos string) string {
	if goos != "darwin" {
		return ""
	}
	return "; on macOS, allow Local Network access for this program in System Settings > Privacy & Security > Local Network, then scan again"
}

func scanSilentHint(goos string) string {
	if goos != "darwin" {
		return ""
	}
	return "; on macOS, if Local Network access is off for this program, allow it in System Settings > Privacy & Security > Local Network"
}
