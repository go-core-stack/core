// Copyright © 2025-2026 Prabhjot Singh Sethi, All Rights reserved
// Author: Prabhjot Singh Sethi <prabhjot.sethi@gmail.com>

// Package settings provides the platform settings record: one record of
// settings that are decided once for the platform and acted on by many
// processes sharing a database.
//
// The package owns its database, as sync owns its own: the record lives in
// the "settings" database, collection "platform_settings", under the key
// {id: "platform"}. Every reader and writer locate it through
// LocateSettingsTable, so they all meet on the same record.
//
// # Sections
//
// The record is a set of sections, one per setting. Each section is owned
// by one writer and written whole by it, as one key of the record, so a
// write of one setting never touches another: a setting decided by one
// process and a setting given by configuration can share the record
// without either overwriting the other. Rules about a section, such as the
// ordering below, apply within that section only.
//
// # Absence means normal
//
// A missing record, a missing section, a missing field and a field's zero
// value all mean that the setting is not set. A reader that has never seen
// the record, and a deployment that never writes one, behave normally.
//
// # Licensing
//
// While licensing.unlicensed is true, services limit what they serve;
// while it is false or missing, they behave normally. Unlicensed reads it
// from the table's cache. Readers that act on a change register with the
// table's reconciler, as for any cached table.
//
// The licensing writer decides each value from inputs that carry their own
// time, and its writes only move forward. SetUnlicensed records, with the
// value, the pair it was decided from: the latest input time (basis, Unix
// seconds) and whether the input at that time was a restricting one
// (basisRefusal). A write applies only when it moves the licensing section
// forward:
//
//   - the record has no licensing section; or
//   - its pair is later than the stored one, compared as time, then
//     false before true; or
//   - its pair equals the stored one, the stored value is not true and the
//     new value is true.
//
// At an equal pair only time has passed, so the value can only go from
// normal to restricted; clearing it always needs a newer input. A writer
// that has seen only older inputs, or that keeps running after it should
// have stopped, gets applied = false and leaves the section as it is.
//
// A write starts as one conditional update. A miss does not tell a missing
// record from one whose section is not behind the write, so on a miss the
// record is inserted; if it exists, created before the miss or by another
// writer since, the conditional update is applied once more and its result
// returned. Writers racing on the first write therefore end at the latest
// pair, and a record created by another section's writer takes the
// licensing section on that second update.
//
// A licensing section edited to have no basis matches no ordering case, so
// it stays as it is until it is corrected outside the library.
package settings
