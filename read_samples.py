import sqlite3
conn = sqlite3.connect('cache.db')
cur = conn.cursor()
cur.execute("SELECT question, question_type, options, answer FROM cache WHERE question_type='single' LIMIT 5")
rows = cur.fetchall()
for r in rows:
    print('Q:', r[0][:80])
    print('T:', r[1])
    print('O:', (r[2] or '')[:120])
    print('A:', r[3])
    print('---')
conn.close()
