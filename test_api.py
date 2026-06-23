import sqlite3
import requests
import time
import json
import uuid

def test_api():
    conn = sqlite3.connect('cache.db')
    cur = conn.cursor()
    cur.execute("SELECT question, question_type, options, answer FROM cache WHERE question_type='single' LIMIT 3")
    rows = cur.fetchall()
    
    url = "http://127.0.0.1:5000/api/search"
    headers = {"Content-Type": "application/json"}
    
    for r in rows:
        q = r[0] + f" (Testing mode {uuid.uuid4().hex[:6]})"
        q_type = r[1]
        opts = r[2]
        
        payload = {
            "title": q,
            "type": q_type,
            "options": opts or ""
        }
        
        try:
            print(f"Testing Question: {q[:60]}...")
            start = time.time()
            resp = requests.post(url, json=payload, headers=headers)
            elapsed = time.time() - start
            if resp.status_code == 200:
                data = resp.json()
                print(f"Response ({elapsed:.2f}s): {data.get('answer')}")
            else:
                print(f"Error ({elapsed:.2f}s): {resp.status_code} - {resp.text}")
        except Exception as e:
            print(f"Request failed: {e}")
        print("-" * 50)
        
    conn.close()

if __name__ == "__main__":
    test_api()
