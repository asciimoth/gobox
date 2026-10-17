package gobox

import (
	"fmt"
	"io/fs"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidateName checks a bounded logical identifier such as a slot, template,
// or program name.
func ValidateName(name string) error {
	if len(name) == 0 || len(name) > 128 {
		return ErrInvalid
	}
	for _, c := range name {
		valid := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
		if !valid {
			return ErrInvalid
		}
	}
	if name == "." || name == ".." {
		return ErrInvalid
	}
	return nil
}

// ValidatePath is a portable syntax check, not filesystem confinement.
// Backends must also reject links and aliases with held native identities.
func ValidatePath(path Path, allowRoot bool) error {
	if path.Root != Temporary && path.Root != Persistent {
		return ErrInvalid
	}
	if path.Rel == "" || path.Rel == "." {
		if allowRoot {
			return nil
		}
		return ErrInvalid
	}
	if len(path.Rel) > 4096 || !utf8.ValidString(path.Rel) {
		return ErrInvalid
	}
	for _, part := range strings.Split(path.Rel, "/") {
		if part == "" || part == "." || part == ".." || len(part) > 255 || strings.TrimRight(part, " .") != part {
			return ErrInvalid
		}
		for _, c := range part {
			if unicode.IsControl(c) || strings.ContainsRune(`\:<>"|?*`, c) {
				return ErrInvalid
			}
		}
		base, _, _ := strings.Cut(part, ".")
		base = strings.ToUpper(strings.TrimRight(base, " "))
		switch base {
		case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
			return ErrInvalid
		}
		if len(base) > 3 && utf8.RuneCountInString(base[3:]) == 1 &&
			(strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && strings.Contains("123456789¹²³", base[3:]) {
			return ErrInvalid
		}
	}
	return nil
}

// Validate checks that an option set describes one valid open operation.
func (options OpenOptions) Validate() error {
	if !options.Read && !options.Write ||
		(options.Create || options.Truncate || options.Append) && !options.Write ||
		options.Exclusive && !options.Create || options.Append && options.Truncate ||
		options.Mode & ^fs.FileMode(0o777) != 0 {
		return ErrInvalid
	}
	return nil
}

// ValidateEndpoint accepts only numeric, unmapped, zone-free loopback IPs.
func ValidateEndpoint(network, address string, listen bool) error {
	switch network {
	case "tcp", "tcp4", "tcp6", "udp", "udp4", "udp6":
	default:
		return ErrUnsupported
	}
	endpoint, err := netip.ParseAddrPort(address)
	if err != nil || endpoint.Addr().Zone() != "" || endpoint.Addr().Is4In6() ||
		!endpoint.Addr().IsLoopback() || !listen && endpoint.Port() == 0 {
		return ErrInvalid
	}
	if strings.HasSuffix(network, "4") && !endpoint.Addr().Is4() ||
		strings.HasSuffix(network, "6") && !endpoint.Addr().Is6() {
		return ErrInvalid
	}
	return nil
}

// Validate rejects non-selected union fields, including zero-valued path tags.
func (value Value) Validate() error {
	remaining := value
	remaining.Kind = ""
	switch value.Kind {
	case TextValue:
		if len(value.Text) > 65536 || !utf8.ValidString(value.Text) || strings.ContainsRune(value.Text, 0) {
			return ErrInvalid
		}
		remaining.Text = ""
	case IntegerValue:
		remaining.Int = 0
	case BooleanValue:
		remaining.Bool = false
	case PathValue:
		if err := ValidatePath(value.Path, true); err != nil {
			return err
		}
		remaining.Path = Path{}
	case ProgramValue:
		if err := ValidateName(string(value.Program)); err != nil {
			return err
		}
		remaining.Program = ""
	case EndpointValue:
		if err := ValidateEndpoint("tcp", value.Endpoint, false); err != nil {
			return err
		}
		remaining.Endpoint = ""
	default:
		return fmt.Errorf("%w: value kind", ErrInvalid)
	}
	if remaining != (Value{}) {
		return fmt.Errorf("%w: non-selected value field", ErrInvalid)
	}
	return nil
}
