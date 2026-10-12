-- The board loads every listing's status history (trumpet_events by listing);
-- without this index each listing's subquery scanned all events.
CREATE INDEX IF NOT EXISTS trumpet_event_listing_idx ON trumpet_events(listing_id,occurred_at);
