-- Add platform pronunciation entries for common technical product names.
-- SQLite store reads the latest platform-default row, so insert a complete
-- successor row containing the existing seed plus the new entries.

INSERT INTO pronunciation_dictionaries (id, tenant_id, name, rules, is_platform_default)
SELECT 'platform-default-technical-terms', NULL, '平台默认发音词典',
       '[
         {"pattern":"GHz","replacement":"吉赫兹","enabled":true},
         {"pattern":"MHz","replacement":"兆赫兹","enabled":true},
         {"pattern":"kHz","replacement":"千赫兹","enabled":true},
         {"pattern":"CUDA","replacement":"库达","enabled":true},
         {"pattern":"FPGA","replacement":"费普加","enabled":true},
         {"pattern":"Spectrum-4","replacement":"Spectrum 四","enabled":true},
         {"pattern":"Spectrum4","replacement":"Spectrum 四","enabled":true},
         {"pattern":"Scale-out","replacement":"Scale out","enabled":true},
         {"pattern":"Scale-Out","replacement":"Scale out","enabled":true},
         {"pattern":"scale out","replacement":"Scale out","enabled":true},
         {"pattern":"CX8","replacement":"C X 八","enabled":true},
         {"pattern":"ConnectX-8","replacement":"Connect X 八","enabled":true},
         {"pattern":"ConnectX8","replacement":"Connect X 八","enabled":true},
         {"pattern":"Pro5000","replacement":"Pro 五千","enabled":true},
         {"pattern":"Pro 5000","replacement":"Pro 五千","enabled":true},
         {"pattern":"RTX PRO 5000","replacement":"RTX PRO 五千","enabled":true},
         {"pattern":"R9000P","replacement":"R 九千 P","enabled":true}
       ]',
       1
WHERE NOT EXISTS (
    SELECT 1 FROM pronunciation_dictionaries WHERE id = 'platform-default-technical-terms'
);
