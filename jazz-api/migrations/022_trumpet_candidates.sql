ALTER TABLE trumpet_listings ADD COLUMN IF NOT EXISTS verification_state text NOT NULL DEFAULT 'verified'
 CHECK (verification_state IN ('verified','candidate','historical'));
UPDATE trumpet_listings SET verification_state='historical'
 WHERE seed_key IS NOT NULL AND last_checked IS NULL AND verification_state='verified';
