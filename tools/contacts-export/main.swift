#!/usr/bin/env swift
//
// contacts-export - Export macOS contacts as JSON
//
// Usage: contacts-export [--help]
//
// Outputs newline-delimited JSON to stdout:
// {"firstName":"John","lastName":"Doe","phones":["14157347847","14157348888"]}
//
// Phone numbers are normalized to digits only (E.164-ish without +).
// Only contacts with at least one phone number are exported.
//

import Contacts
import Foundation

struct Contact: Codable {
    let firstName: String
    let lastName: String
    let fullName: String
    let phones: [String]
}

/// Normalize phone number to digits only, strip leading + and 00
func normalizePhone(_ phone: String) -> String {
    let digits = phone.unicodeScalars.filter { CharacterSet.decimalDigits.contains($0) }
    var result = String(digits)
    
    // Strip leading zeros (international 00 prefix)
    while result.hasPrefix("0") {
        result = String(result.dropFirst())
    }
    
    return result
}

func exportContacts() {
    let store = CNContactStore()
    
    // Check authorization status first
    let status = CNContactStore.authorizationStatus(for: .contacts)
    
    switch status {
    case .authorized, .limited:
        break // Good to go
    case .notDetermined:
        // Request access synchronously
        let semaphore = DispatchSemaphore(value: 0)
        var accessGranted = false
        
        store.requestAccess(for: .contacts) { granted, error in
            accessGranted = granted
            if let error = error {
                fputs("Error requesting access: \(error.localizedDescription)\n", stderr)
            }
            semaphore.signal()
        }
        _ = semaphore.wait(timeout: .now() + 30)
        
        guard accessGranted else {
            fputs("Error: Contacts access denied. Grant access in System Settings > Privacy & Security > Contacts.\n", stderr)
            exit(1)
        }
    case .denied, .restricted:
        fputs("Error: Contacts access denied. Grant access in System Settings > Privacy & Security > Contacts.\n", stderr)
        exit(1)
    @unknown default:
        fputs("Error: Unknown authorization status.\n", stderr)
        exit(1)
    }
    
    // Fetch all contacts with phone numbers
    let keysToFetch: [CNKeyDescriptor] = [
        CNContactGivenNameKey as CNKeyDescriptor,
        CNContactFamilyNameKey as CNKeyDescriptor,
        CNContactPhoneNumbersKey as CNKeyDescriptor,
    ]
    
    let request = CNContactFetchRequest(keysToFetch: keysToFetch)
    let encoder = JSONEncoder()
    
    do {
        try store.enumerateContacts(with: request) { contact, _ in
            // Skip contacts without phone numbers
            guard !contact.phoneNumbers.isEmpty else { return }
            
            // Normalize phone numbers
            let phones = contact.phoneNumbers.compactMap { phone -> String? in
                let normalized = normalizePhone(phone.value.stringValue)
                // Skip very short numbers (likely invalid)
                guard normalized.count >= 7 else { return nil }
                return normalized
            }
            
            // Skip if no valid phones after normalization
            guard !phones.isEmpty else { return }
            
            // Build full name
            var fullName = ""
            if !contact.givenName.isEmpty {
                fullName = contact.givenName
            }
            if !contact.familyName.isEmpty {
                if !fullName.isEmpty { fullName += " " }
                fullName += contact.familyName
            }
            
            let c = Contact(
                firstName: contact.givenName,
                lastName: contact.familyName,
                fullName: fullName,
                phones: phones
            )
            
            if let jsonData = try? encoder.encode(c),
               let jsonString = String(data: jsonData, encoding: .utf8) {
                print(jsonString)
            }
        }
    } catch {
        fputs("Error fetching contacts: \(error.localizedDescription)\n", stderr)
        exit(1)
    }
}

// Main
if CommandLine.arguments.contains("--help") || CommandLine.arguments.contains("-h") {
    print("""
        contacts-export - Export macOS contacts as JSON
        
        Usage: contacts-export [--help]
        
        Outputs newline-delimited JSON to stdout. Each line is a contact:
        {"firstName":"John","lastName":"Doe","fullName":"John Doe","phones":["14157347847"]}
        
        Only contacts with phone numbers are exported.
        Phone numbers are normalized to digits only.
        
        Requires Contacts access permission (will prompt on first run).
        """)
    exit(0)
}

exportContacts()
