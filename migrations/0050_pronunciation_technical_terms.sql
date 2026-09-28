-- Add conservative platform pronunciation entries for common technical product
-- names whose natural Chinese-TTS reading differs from automatic model guards.

WITH latest AS (
    SELECT id
      FROM pronunciation_dictionaries
     WHERE is_platform_default = true
     ORDER BY created_at DESC, id DESC
     LIMIT 1
), additions(pattern, replacement) AS (
    VALUES
      ('Spectrum-4', 'Spectrum 四'),
      ('Spectrum4', 'Spectrum 四'),
      ('Scale-out', 'Scale out'),
      ('Scale-Out', 'Scale out'),
      ('scale out', 'Scale out'),
      ('CX8', 'C X 八'),
      ('ConnectX-8', 'Connect X 八'),
      ('ConnectX8', 'Connect X 八'),
      ('Pro5000', 'Pro 五千'),
      ('Pro 5000', 'Pro 五千'),
      ('RTX PRO 5000', 'RTX PRO 五千'),
      ('R9000P', 'R 九千 P')
), missing AS (
    SELECT jsonb_agg(jsonb_build_object('pattern', a.pattern, 'replacement', a.replacement, 'enabled', true)) AS rules
      FROM additions a
      CROSS JOIN latest l
     WHERE NOT EXISTS (
        SELECT 1
          FROM pronunciation_dictionaries d,
               jsonb_array_elements(d.rules) elem
         WHERE d.id = l.id
           AND elem->>'pattern' = a.pattern
     )
)
UPDATE pronunciation_dictionaries d
   SET rules = d.rules || missing.rules,
       updated_at = now()
  FROM latest, missing
 WHERE d.id = latest.id
   AND missing.rules IS NOT NULL;
