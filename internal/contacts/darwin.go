//go:build darwin

package contacts

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

// SystemContact represents a contact from macOS Contacts.app
type SystemContact struct {
	FirstName string   `json:"firstName"`
	LastName  string   `json:"lastName"`
	FullName  string   `json:"fullName"`
	Phones    []string `json:"phones"`
}

// Name returns the best display name for the contact
func (c SystemContact) Name() string {
	if c.FullName != "" {
		return c.FullName
	}
	parts := []string{}
	if c.FirstName != "" {
		parts = append(parts, c.FirstName)
	}
	if c.LastName != "" {
		parts = append(parts, c.LastName)
	}
	return strings.Join(parts, " ")
}

// GetSystemContacts fetches all contacts with phone numbers from macOS Contacts.app
// Tries Swift helper first (proper API), falls back to direct SQLite (faster, no permissions)
func GetSystemContacts() ([]SystemContact, error) {
	// Try Swift helper first (uses CNContactStore - proper macOS API)
	contacts, err := getContactsViaSwiftHelper()
	if err == nil && len(contacts) > 0 {
		return contacts, nil
	}

	// Fallback to direct SQLite access (works without permissions but uses private API)
	return getContactsViaSQLite()
}

// getContactsViaSwiftHelper uses the contacts-export Swift tool
func getContactsViaSwiftHelper() ([]SystemContact, error) {
	// Find the helper binary (check common locations)
	helperPaths := []string{
		"contacts-export", // In PATH
	}

	// Check relative to executable
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		helperPaths = append(helperPaths,
			filepath.Join(dir, "contacts-export"),
			filepath.Join(dir, "..", "tools", "contacts-export", "contacts-export"),
		)
	}

	// Check in wacli tools directory
	if home, err := os.UserHomeDir(); err == nil {
		helperPaths = append(helperPaths,
			filepath.Join(home, ".wacli", "tools", "contacts-export"),
			filepath.Join(home, "github", "wacli", "tools", "contacts-export", "contacts-export"),
		)
	}

	var helperPath string
	for _, p := range helperPaths {
		if _, err := exec.LookPath(p); err == nil {
			helperPath = p
			break
		}
		if _, err := os.Stat(p); err == nil {
			helperPath = p
			break
		}
	}

	if helperPath == "" {
		return nil, fmt.Errorf("contacts-export helper not found")
	}

	cmd := exec.Command(helperPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("create pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start helper: %w", err)
	}

	var contacts []SystemContact
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		var c SystemContact
		if err := json.Unmarshal(scanner.Bytes(), &c); err != nil {
			continue
		}
		if c.Name() != "" && len(c.Phones) > 0 {
			contacts = append(contacts, c)
		}
	}

	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("helper failed: %w", err)
	}

	return contacts, nil
}

// findAddressBookDBs finds all AddressBook SQLite databases on macOS
func findAddressBookDBs() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	var dbs []string
	abPath := filepath.Join(home, "Library", "Application Support", "AddressBook")

	// Check main database
	mainDB := filepath.Join(abPath, "AddressBook-v22.abcddb")
	if _, err := os.Stat(mainDB); err == nil {
		dbs = append(dbs, mainDB)
	}

	// Check Sources folder for synced databases (iCloud, Exchange, etc.)
	sourcesPath := filepath.Join(abPath, "Sources")
	entries, err := os.ReadDir(sourcesPath)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				sourceDB := filepath.Join(sourcesPath, entry.Name(), "AddressBook-v22.abcddb")
				if _, err := os.Stat(sourceDB); err == nil {
					dbs = append(dbs, sourceDB)
				}
			}
		}
	}

	return dbs, nil
}

// getContactsViaSQLite reads directly from AddressBook SQLite databases
// This is a fallback - it works without permissions but uses undocumented schema
func getContactsViaSQLite() ([]SystemContact, error) {
	dbPaths, err := findAddressBookDBs()
	if err != nil {
		return nil, fmt.Errorf("find address book databases: %w", err)
	}
	if len(dbPaths) == 0 {
		return nil, fmt.Errorf("no AddressBook database found")
	}

	// Aggregate contacts from all databases, keyed by first+last name to avoid duplicates
	contactMap := make(map[string]*SystemContact)

	for _, dbPath := range dbPaths {
		// Open in read-only mode with immutable flag to avoid locking issues
		db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?mode=ro&immutable=1", dbPath))
		if err != nil {
			continue // Skip databases we can't open
		}

		// Query contacts with phone numbers
		rows, err := db.Query(`
			SELECT 
				COALESCE(r.ZFIRSTNAME, ''),
				COALESCE(r.ZLASTNAME, ''),
				p.ZFULLNUMBER
			FROM ZABCDRECORD r
			JOIN ZABCDPHONENUMBER p ON p.ZOWNER = r.Z_PK
			WHERE p.ZFULLNUMBER IS NOT NULL 
			  AND p.ZFULLNUMBER != ''
			  AND (r.ZFIRSTNAME IS NOT NULL OR r.ZLASTNAME IS NOT NULL)
		`)
		if err != nil {
			db.Close()
			continue
		}

		for rows.Next() {
			var firstName, lastName, phone string
			if err := rows.Scan(&firstName, &lastName, &phone); err != nil {
				continue
			}

			key := firstName + "|" + lastName
			if existing, ok := contactMap[key]; ok {
				// Add phone to existing contact
				existing.Phones = append(existing.Phones, phone)
			} else {
				c := &SystemContact{
					FirstName: firstName,
					LastName:  lastName,
					Phones:    []string{phone},
				}
				c.FullName = c.Name()
				contactMap[key] = c
			}
		}
		rows.Close()
		db.Close()
	}

	// Convert map to slice
	contacts := make([]SystemContact, 0, len(contactMap))
	for _, c := range contactMap {
		if c.Name() != "" && len(c.Phones) > 0 {
			contacts = append(contacts, *c)
		}
	}

	return contacts, nil
}

// NormalizePhone strips non-digit characters and normalizes phone numbers
// Returns the normalized number suitable for WhatsApp JID matching
func NormalizePhone(phone string) string {
	// Remove all non-digit characters except leading +
	re := regexp.MustCompile(`[^\d+]`)
	normalized := re.ReplaceAllString(phone, "")

	// Remove leading + if present (WhatsApp JIDs don't have it)
	normalized = strings.TrimPrefix(normalized, "+")

	// Remove leading zeros (international format)
	normalized = strings.TrimLeft(normalized, "0")

	return normalized
}

// BuildPhoneToNameMap creates a map from normalized phone numbers to contact names
func BuildPhoneToNameMap(contacts []SystemContact) map[string]string {
	result := make(map[string]string)
	for _, c := range contacts {
		name := c.Name()
		if name == "" {
			continue
		}
		for _, phone := range c.Phones {
			normalized := NormalizePhone(phone)
			if normalized != "" && len(normalized) >= 7 { // Skip very short numbers
				// Don't overwrite if we already have a name for this number
				if _, exists := result[normalized]; !exists {
					result[normalized] = name
				}
			}
		}
	}
	return result
}
