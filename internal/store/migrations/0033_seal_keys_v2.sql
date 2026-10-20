-- 0033_seal_keys_v2.sql: ML-DSA-65 seal keys beside Ed25519, and room for dual-sealed deeds
-- (deed v2, ADR-109, TY-02).
--
-- 1. seal_keys.alg accepts ML-DSA-65 (FIPS 204), whose public key is 1,952 bytes; an Ed25519 key
--    stays 32 bytes. The thumbprint check (sha256 of public_key) is unchanged.
-- 2. deeds.envelope holds up to 24,576 bytes: JCS(envelope) of a v2 deed, whose ML-DSA-65 seal is
--    4,412 characters (v1 deeds stay at most 16,384 bytes; the Sealer refuses larger ones). The seal
--    and seal_key_id columns keep the primary (Ed25519) seal, so their checks are unchanged.

ALTER TABLE deedglass.seal_keys
  DROP CONSTRAINT seal_keys_alg_check,
  DROP CONSTRAINT seal_keys_public_key_check,
  ADD CONSTRAINT seal_keys_alg_check CHECK (alg IN ('Ed25519', 'ML-DSA-65')),
  ADD CONSTRAINT seal_keys_public_key_check CHECK (
    (alg = 'Ed25519' AND pg_catalog.octet_length(public_key) = 32)
    OR (alg = 'ML-DSA-65' AND pg_catalog.octet_length(public_key) = 1952));

ALTER TABLE deedglass.deeds
  DROP CONSTRAINT deeds_envelope_check,
  ADD CONSTRAINT deeds_envelope_check CHECK (pg_catalog.octet_length(envelope) <= 24576);
