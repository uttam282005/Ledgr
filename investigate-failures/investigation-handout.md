# Investigation Handout — Reconciliation Engine vs. Messy Sample Data

## Purpose

This handout pairs the three sample CSVs (`internal_ledger_messy.csv`, `gateway_settlement_messy.csv`, `bank_statement_messy.csv`) with the exact ground truth used to generate them, and a checklist of properties the engine must satisfy. If a reconciliation run against these files produces different numbers than the ground truth below, use the property checklist to localize which stage — mapping, parsing, Hop 1, or Hop 2 — is responsible, rather than debugging the whole pipeline at once.

**How to use this document when investigating a failure:**
1. Run the engine against the three CSVs.
2. Diff its output against the ground truth tables below, record by record.
3. Find the specific property in Section 3 that covers the mismatching record type.
4. Fix that property in isolation, re-run, re-diff.

---

## 1. Sample input files

| File | Rows | Header format |
|---|---|---|
| `internal_ledger_messy.csv` | 50 | `Txn ID, Amount, Txn Date, Merchant Code, Ref` — ₹ symbol + commas, DD-MM-YYYY |
| `gateway_settlement_messy.csv` | 52 | `SettlementRef, Net Settled, Value Date, MID, Batch No, OrderRef` — plain numeric + commas, YYYY/MM/DD |
| `bank_statement_messy.csv` | 10 | `Sl No, Txn Description, Credit Amount, Txn Date, Merchant Code, Batch Reference` — plain numeric + commas, Unix epoch |

Note: `bank_statement_messy.csv`'s `Sl No` column is a sequential row counter, not a real record ID — see Property 6.

---

## 2. Ground truth answer key

### 2a. Internal ledger — expected category per record

| ID | Category | Amount (₹) | Merchant |
|---|---|---|---|
| INT001 | clean | 2773.89 | M-D |
| INT002 | clean | 4181.14 | M-C |
| INT003 | clean | 533.86 | M-B |
| INT004 | clean | 583.00 | M-B |
| INT005 | clean | 1534.27 | M-C |
| INT006 | **duplicate** | 1205.63 | M-C |
| INT007 | clean | 3310.57 | M-B |
| INT008 | clean | 984.33 | M-D |
| INT009 | clean | 3272.00 | M-B |
| INT010 | clean | 3887.91 | M-A |
| INT011 | clean | 4064.22 | M-D |
| INT012 | clean | 4583.05 | M-C |
| INT013 | clean | 2099.03 | M-D |
| INT014 | clean | 1383.81 | M-C |
| INT015 | **mismatch** | 4509.55 | M-D |
| INT016 | **mismatch** | 4988.18 | M-D |
| INT017 | clean | 4333.29 | M-B |
| INT018 | clean | 3466.31 | M-A |
| INT019 | **mismatch** | 4981.38 | M-C |
| INT020 | clean | 255.11 | M-A |
| INT021 | **date_drift** | 3804.21 | M-C |
| INT022 | clean | 959.16 | M-A |
| INT023 | clean | 1464.27 | M-B |
| INT024 | **orphan_internal** | 4378.49 | M-C |
| INT025 | **date_drift** | 3123.06 | M-B |
| INT026 | clean | 2789.02 | M-A |
| INT027 | clean | 293.49 | M-C |
| INT028 | clean | 4072.52 | M-B |
| INT029 | clean | 2923.31 | M-A |
| INT030 | **date_drift** | 4116.91 | M-B |
| INT031 | **duplicate** | 4744.87 | M-B |
| INT032 | **mismatch** | 3111.65 | M-B |
| INT033 | clean | 3703.67 | M-B |
| INT034 | **duplicate** | 4976.72 | M-C |
| INT035 | clean | 2367.13 | M-B |
| INT036 | **orphan_internal** | 300.96 | M-B |
| INT037 | **orphan_internal** | 540.77 | M-A |
| INT038 | clean | 350.78 | M-C |
| INT039 | clean | 1536.69 | M-D |
| INT040 | **mismatch** | 3672.09 | M-D |
| INT041 | clean | 4075.99 | M-B |
| INT042 | clean | 2269.05 | M-D |
| INT043 | clean | 3699.56 | M-A |
| INT044 | clean | 1828.65 | M-A |
| INT045 | clean | 2774.17 | M-B |
| INT046 | clean | 2420.61 | M-A |
| INT047 | clean | 4308.64 | M-A |
| INT048 | clean | 2794.82 | M-A |
| INT049 | clean | 4273.74 | M-B |
| INT050 | **date_drift** | 1225.99 | M-D |

**Expected Hop 1 tallies:** 35 clean → `FULL` match · 5 mismatch → `AMOUNT_MISMATCH` · 4 date_drift → `FULL` match (confidence 0.7, extended window) · 3 orphan_internal → `NO_COUNTERPART` · 3 duplicate → each has 2 settlement rows, both must surface as `DUPLICATE_SETTLEMENT`.

### 2b. Settlement records — internal_id mapping (for cross-checking Hop 1)

| Settlement ID | Maps to internal_id | Note |
|---|---|---|
| SET001 | INT001 | clean |
| SET002 | INT002 | clean |
| SET003 | INT003 | clean |
| SET004 | INT004 | clean |
| SET005 | INT005 | clean |
| SET006 | INT006 | duplicate (pair with SET007) |
| SET007 | INT006 | duplicate (pair with SET006) |
| SET008 | INT007 | clean |
| SET009 | INT008 | clean |
| SET010 | INT009 | clean |
| SET011 | INT010 | clean |
| SET012 | INT011 | clean |
| SET013 | INT012 | clean |
| SET014 | INT013 | clean |
| SET015 | INT014 | clean |
| SET016 | INT015 | mismatch |
| SET017 | INT016 | mismatch |
| SET018 | INT017 | clean |
| SET019 | INT018 | clean |
| SET020 | INT019 | mismatch |
| SET021 | INT020 | clean |
| SET022 | INT021 | date_drift |
| SET023 | INT022 | clean |
| SET024 | INT023 | clean |
| SET025 | INT025 | date_drift |
| SET026 | INT026 | clean |
| SET027 | INT027 | clean |
| SET028 | INT028 | clean |
| SET029 | INT029 | clean |
| SET030 | INT030 | date_drift |
| SET031 | INT031 | duplicate (pair with SET032) |
| SET032 | INT031 | duplicate (pair with SET031) |
| SET033 | INT032 | mismatch |
| SET034 | INT033 | clean |
| SET035 | INT034 | duplicate (pair with SET036) |
| SET036 | INT034 | duplicate (pair with SET035) |
| SET037 | INT035 | clean |
| SET038 | INT038 | clean |
| SET039 | INT039 | clean |
| SET040 | INT040 | mismatch |
| SET041 | INT041 | clean |
| SET042 | INT042 | clean |
| SET043 | INT043 | clean |
| SET044 | INT044 | clean |
| SET045 | INT045 | clean |
| SET046 | INT046 | clean |
| SET047 | INT047 | clean |
| SET048 | INT048 | clean |
| SET049 | INT049 | clean |
| SET050 | INT050 | date_drift |
| SET051 | *(none)* | **orphan_settlement** |
| SET052 | *(none)* | **orphan_settlement** |

INT024, INT036, INT037 have **no row** in this table at all — confirms `NO_COUNTERPART`.

### 2c. Batch ground truth (for cross-checking Hop 2)

| Batch | Status | Settlement sum (₹) | Members | Bank row |
|---|---|---|---|---|
| BATCH001 | CLEAN | 12,030.57 | SET052, SET016, SET037, SET034, SET027 | BNK008 |
| BATCH002 | CLEAN | 12,113.62 | SET021, SET051, SET006, SET032, SET044 | BNK005 |
| BATCH003 | CLEAN | 15,138.67 | SET025, SET015, SET013, SET033, SET019 | BNK003 |
| BATCH004 | CLEAN | 13,643.91 | SET023, SET042, SET014, SET018, SET030 | BNK006 |
| BATCH005 | CLEAN | 9,514.18 | SET004, SET038, SET043, SET022, SET007 | BNK002 |
| BATCH006 | **NOT_BANKED** | 19,000.75 | SET031, SET002, SET040, SET026, SET049 | *(none — expect `SETTLED_NOT_BANKED`)* |
| BATCH007 | CLEAN | 15,299.92 | SET012, SET020, SET039, SET009, SET047 | BNK009 |
| BATCH008 | CLEAN | 13,658.79 | SET041, SET045, SET024, SET003, SET035 | BNK007 |
| BATCH009 | CLEAN | 12,009.64 | SET010, SET005, SET008, SET001, SET050 | BNK004 |
| BATCH010 | **PARTIAL** | 16,993.01 | SET028, SET046, SET036, SET029, SET048 | BNK001 — credited **less** than 16,993.01, expect `PARTIAL_CREDIT` with a specific shortfall |
| BATCH011 | **NOT_BANKED** | 8,402.50 | SET011, SET017 | *(none — expect `SETTLED_NOT_BANKED`)* |

**Orphan bank row (BANKED_NOT_SETTLED):** `BNK010`, `batch_ref = BATCH913`, amount ₹1,991.43, merchant M-C — this batch reference does not exist anywhere in the settlement file. Must surface as an orphan bank credit, not silently dropped or crash a lookup.

**Expected Hop 2 tallies:** 8 batches CLEAN (batch-reference or aggregated match) · 2 batches `SETTLED_NOT_BANKED` (BATCH006, BATCH011) · 1 batch `PARTIAL_CREDIT` (BATCH010) · 1 orphan bank row `BANKED_NOT_SETTLED` (BNK010).

---

## 3. Property checklist (fix in this order if multiple fail)

Check these roughly in pipeline order — a failure early (mapping/parsing) will cascade into every downstream property, so fix upstream first and re-run before chasing a downstream symptom that may just be noise from an earlier bug.

### Stage 1 — Column mapping
1. Internal headers (`Txn ID`, `Amount`, `Txn Date`, `Merchant Code`, `Ref`) map to (`id`, `amount`, `transaction_date`, `merchant_id`, `reference_id`).
2. Settlement headers (`SettlementRef`, `Net Settled`, `Value Date`, `MID`, `Batch No`, `OrderRef`) map to (`id`, `settled_amount`, `settlement_date`, `merchant_id`, `batch_id`, `reference_id`).
3. Bank headers (`Sl No`, `Txn Description`, `Credit Amount`, `Txn Date`, `Merchant Code`, `Batch Reference`) map to (unmapped or auto-id, `narration`, `credited_amount`, `credit_date`, `merchant_id`, `batch_reference`).
4. **`Sl No` must not be mapped to `id`** — it's a row counter (1, 2, 3...), not a business identifier. If the engine trusts it as `id`, every bank row's identity is meaningless and any downstream audit trail referencing "bank record 3" is fragile.
5. Empty `Ref`/`OrderRef` values (most rows) must not error or be treated as a literal empty-string reference that accidentally matches another empty-string reference elsewhere.

### Stage 2 — Value parsing
6. Currency stripping: `₹2,773.89` → same numeric value as `2,773.89` (settlement, no symbol).
7. Date parsing, three distinct formats, resolved **per file**: `03-06-2026` = DD-MM-YYYY (internal), `2026/06/21` = YYYY/MM/DD (settlement), `1781222400` = Unix epoch (bank). Verify a few by hand: `1781222400` → 2026-06-13 (BNK008's underlying batch date — check against BATCH001's max settlement date + 0-2 day offset).
8. All amounts converted to exact integer paise — verify BATCH010's settlement sum (16,993.01 → 1699301 paise) sums exactly from its 5 members with no float drift.

### Stage 3 — Hop 1 (internal ↔ settlement)
9. All 35 `clean` records reach `FULL` match status.
10. All 5 `mismatch` records (INT015, INT016, INT019, INT032, INT040) are rejected by the amount-tolerance check and classified `AMOUNT_MISMATCH` — not silently matched.
11. All 4 `date_drift` records (INT021, INT025, INT030, INT050) match via the extended window at confidence 0.7, not 0.9 or unmatched.
12. All 3 `duplicate` records (INT006, INT031, INT034) produce **two** flagged `DUPLICATE_SETTLEMENT` exceptions each (6 total), not one arbitrarily discarded.
13. All 3 `orphan_internal` records (INT024, INT036, INT037) classify as `NO_COUNTERPART`.
14. Both `orphan_settlement` records (SET051, SET052) classify as `ORPHAN_SETTLEMENT`.

### Stage 4 — Hop 2 (settlement ↔ bank)
15. Batch sums computed correctly per batch (cross-check any batch against Section 2c's stated sum).
16. 8 clean batches match via batch-reference or aggregated-amount rule.
17. BATCH006 and BATCH011 classify `SETTLED_NOT_BANKED` — no bank row exists for either.
18. BATCH010 classifies `PARTIAL_CREDIT` with a computed shortfall equal to `16,993.01 − (BNK001's credited amount)`, exact to the paisa.
19. BNK010 surfaces as an orphan bank credit, `BANKED_NOT_SETTLED`, and does not crash any batch lookup by matching against a nonexistent `BATCH913` in the settlement data.

### Stage 5 — Validation
20. Confirm `merchant_id` is present after mapping for all three sources before reconciliation runs — this was a real bug in an earlier version of the bank CSV (missing merchant column entirely) and should fail loudly at ingestion, not silently null out the merchant check in Hop 2.

---

## 4. Quick sanity totals (if the engine reports aggregate stats only)

- Hop 1 match rate: **39 of 50** internal transactions should reach `FULL` (35 clean + 4 date_drift) → **78%**. 5 mismatch + 3 orphan_internal + 3 duplicate = 11 records in exception states.
- Hop 2: **8 of 11** batches fully reconciled → ~**73%** batch-level full-chain rate.
- Total distinct exception rows expected across both hops: 5 (`AMOUNT_MISMATCH`) + 6 (`DUPLICATE_SETTLEMENT`, 2 per duplicate ×3) + 3 (`NO_COUNTERPART`) + 2 (`ORPHAN_SETTLEMENT`) + 2 (`SETTLED_NOT_BANKED`) + 1 (`PARTIAL_CREDIT`) + 1 (`BANKED_NOT_SETTLED`) = **20 exception rows** total.

If your engine's total exception count differs from 20, that's the fastest single number to start the investigation from — work backward from which category is over- or under-counted using Section 3.
