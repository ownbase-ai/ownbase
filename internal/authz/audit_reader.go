package authz

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// ReadRecentRecords reads the last n records from the JSON Lines audit log at
// path. Returns a partial slice (no error) when the file has fewer than n
// records. Returns nil (not an error) when the file does not exist.
func ReadRecentRecords(path string, n int) ([]AuditRecord, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read audit log: %w", err)
	}
	defer f.Close()

	var all []AuditRecord
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var r AuditRecord
		if err := json.Unmarshal(line, &r); err != nil {
			continue // skip malformed lines
		}
		all = append(all, r)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan audit log: %w", err)
	}

	if n <= 0 || n >= len(all) {
		return all, nil
	}
	return all[len(all)-n:], nil
}
