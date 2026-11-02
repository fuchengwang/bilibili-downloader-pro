package updater

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Installation logs contain local paths and stages, never download URLs or
// activation information. Logging failure must not prevent recovery.
func installEvent(directory, stage, format string, args ...any) {
	f, err := os.OpenFile(filepath.Join(directory, "install.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s stage=%s %s\n", time.Now().UTC().Format(time.RFC3339Nano), stage, fmt.Sprintf(format, args...))
}
