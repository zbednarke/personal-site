-- Body and Soul joins the ballads (#103). New users get it from
-- repertoireSeeds; users seeded before it existed get it here, once, at the
-- end of their ballads. The marker keeps a later archive or rename from being
-- undone on restart.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM jazz_backfills WHERE name = '044_repertoire_body_and_soul') THEN
    WITH added AS (
      INSERT INTO repertoire_tunes (user_id,tune_id,title,category,chosen,position)
      SELECT s.user_id, 'body-and-soul', 'Body and Soul', 'ballad', true,
             LEAST(999, COALESCE((SELECT MAX(t.position) + 1 FROM repertoire_tunes t
                                   WHERE t.user_id = s.user_id AND t.category = 'ballad'), 0))
        FROM repertoire_settings s
      ON CONFLICT (user_id,tune_id) DO NOTHING
      RETURNING user_id
    )
    INSERT INTO repertoire_events (user_id,tune_id,event_type,payload)
    SELECT user_id, 'body-and-soul', 'tune.seeded', '{"title":"Body and Soul","category":"ballad"}'::jsonb FROM added;
    INSERT INTO jazz_backfills(name) VALUES ('044_repertoire_body_and_soul');
  END IF;
END $$;
