-- R3 (ADR-0028): a part signature describes what a client promised, never what object
-- storage accepted. A signature can be issued and its PUT interrupted, and a second
-- signature for the same part number replaces the first without touching storage. R2
-- merged this row into the part listing as if it were proof of stored bytes, which let a
-- resume skip a part that still held another file's content.
--
-- No column changes: the table's shape was never the problem, its meaning was. The rows
-- stay (they are the audit trail for "which digest did the client declare, and when"), and
-- every historical session keeps its recorded signatures. Nothing reads them back as
-- integrity evidence any more; the part listing reports only what storage itself attests.
COMMENT ON TABLE upload_part_signature IS
    'Client-declared part digests at signing time (ADR-0028). Audit only: never proof of stored bytes; resume relies on storage-reported checksums.';
