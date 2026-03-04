# זיכרון ותובנות

## פילוסופיה

רוב עוזרי ה-AI או שוכחים הכל בין סשנים או ממציאים "זכרונות" מנתוני אימון. Iulita נוקט גישה שונה מהותית:

- **אחסון מפורש בלבד** — עובדות נשמרות רק כשאתה מבקש ("תזכור ש...")
- **נתונים מאומתים** — כל עובדה ניתנת למעקב חזרה לבקשת משתמש ספציפית
- **תובנות הצלבה** — דפוסים מתגלים על ידי ניתוח העובדות בפועל
- **דעיכה זמנית** — זכרונות ישנים מאבדים רלוונטיות באופן טבעי אלא אם ניגשים אליהם
- **אחזור היברידי** — חיפוש טקסט מלא FTS5 בשילוב עם הטבעות וקטוריות ONNX

## עובדות

### אחסון עובדות (זכירה)

כשאתה אומר "תזכור ששם הכלב שלי הוא מקס", קורה הדבר הבא:

1. **זיהוי טריגר** — העוזר מזהה את מילת המפתח לזיכרון ("remember") ומכריח את הכלי `remember`
2. **בדיקת כפילויות** — חיפוש בעובדות קיימות באמצעות 3 המילים הראשונות לזיהוי כפילויות קרובות
3. **SQLite INSERT** — העובדה נשמרת עם `user_id`, `content`, `source_type=user`, חותמות זמן
4. **אינדקס FTS5** — טריגר `AFTER INSERT` מוסיף אוטומטית את העובדה לאינדקס חיפוש טקסט מלא `facts_fts`
5. **הטבעה וקטורית** — goroutine רקע מייצר הטבעת ONNX (384 ממדים, all-MiniLM-L6-v2) ושומר ב-`fact_vectors`

```
domain.Fact {
    ID             int64
    ChatID         string     // ערוץ מקור ("123456789", "console", "web:uuid")
    UserID         string     // UUID של iulita — משותף בין כל הערוצים
    Content        string     // טקסט העובדה
    SourceType     string     // "user" (מפורש) או "system" (נשלף אוטומטית)
    CreatedAt      time.Time
    LastAccessedAt time.Time  // מתאפס בכל אחזור
    AccessCount    int        // עולה בכל אחזור
}
```

### אחזור עובדות (Recall)

כשאתה שואל "מה שם הכלב שלי?":

1. **חיפוש מוגבל למשתמש** — מנסה תחילה `SearchFactsByUser(userID, query, limit)` לעובדות חוצות ערוצים
2. **התאמת FTS5** — `SELECT * FROM facts WHERE id IN (SELECT rowid FROM facts_fts WHERE facts_fts MATCH ?)`
3. **דגימת יתר** — שולף `limit * 3` מועמדים (מינימום 20) לדירוג חוזר
4. **דעיכה זמנית** — כל מועמד מקבל ניקוד: `decay = exp(-ln(2) / halfLife * ageDays) * (1 + log(1 + accessCount))`
5. **דירוג MMR** — Maximal Marginal Relevance מפחית כפילויות קרובות בתוצאות
6. **חיזוק** — כל עובדה שהוחזרה מקבלת `access_count++` ו-`last_accessed_at = now`

העוזר מבצע גם **חיפוש היברידי** בכל הודעה (לא רק באחזור מפורש):

```
1. ניקוי שאילתה (הסרת אופרטורי FTS, מילות עצירה, הגבלה ל-5 מילות מפתח)
2. יצירת וקטור שאילתה דרך הטבעת ONNX
3. תוצאות FTS: ניקוד מבוסס מיקום (1 - i/(n+1))
4. תוצאות וקטוריות: דמיון קוסינוס מול כל הוקטורים המאוחסנים
5. שילוב: (1-vectorWeight)*ftsScore + vectorWeight*vecScore
6. איחוד שני הסטים, ממוינים לפי ניקוד משולב
```

### שכחת עובדות

הכלי `forget` מוחק עובדה לפי ID. טריגר ה-FTS (`facts_ad`) מסיר אותה אוטומטית מאינדקס הטקסט המלא. `ON DELETE CASCADE` על `fact_vectors` מסיר את ההטבעה.

## דעיכה זמנית

עובדות ותובנות דועכות לאורך זמן באמצעות דעיכה אקספוננציאלית (רדיואקטיבית):

```
decay_factor = exp(-ln(2) / halfLifeDays * ageDays)
```

| ימים מאז גישה | זמן מחצית חיים = 30 יום | זמן מחצית חיים = 90 יום |
|-------------------|---------------------|---------------------|
| 0 | 1.00 | 1.00 |
| 15 | 0.71 | 0.89 |
| 30 | 0.50 | 0.79 |
| 60 | 0.25 | 0.63 |
| 90 | 0.13 | 0.50 |

**החלטות עיצוב מרכזיות:**
- **עובדות** דועכות מ-`last_accessed_at` — כל אחזור מאפס את השעון
- **תובנות** דועכות מ-`created_at` — יש להן תוחלת חיים קבועה
- **הגברת גישה**: `1 + log(1 + accessCount)` — עובדה שנגישה 100 פעמים מקבלת הגברה של פי 4.6
- **זמן מחצית חיים ברירת מחדל**: 30 יום (ניתן להגדרה דרך `skills.memory.half_life_days`)

## דירוג MMR

לאחר ניקוד דעיכה זמנית, Maximal Marginal Relevance מונע תוצאות כפולות כמעט:

```
MMR(item) = lambda * relevance_score - (1 - lambda) * max_similarity_to_selected
```

- `lambda = 1.0` → רלוונטיות טהורה, ללא גיוון
- `lambda = 0.7` → מומלץ: העדפת רלוונטיות אך עם קנס על כפילויות קרובות
- `lambda = 0.0` → גיוון טהור

הדמיון נמדד באמצעות דמיון Jaccard על טוקני מילים (קירוב ללא תלויות שפועל ללא ספק ההטבעות ONNX).

**הגדרה**: `skills.memory.mmr_lambda` (ברירת מחדל 0, מושבת). הגדר ל-0.7 לתוצאות מיטביות.

## תובנות

תובנות הן הצלבות שנוצרות על ידי AI בין העובדות שלך, המתגלות על ידי המתזמן ברקע.

### צינור יצירת תובנות

משימת יצירת תובנות רצה כל 24 שעות (ניתן להגדרה):

```
1. טעינת כל העובדות עבור המשתמש
2. בדיקת מספר עובדות מינימלי (ברירת מחדל 20)
3. בניית וקטורי TF-IDF
   - טוקניזציה: אותיות קטנות, הסרת סימני פיסוק, סינון מילות עצירה
   - יצירת bigramים (זוגות מילים סמוכות)
   - חישוב ניקודי TF-IDF
4. אשכולות K-means++
   - k = sqrt(numFacts / 3)
   - מדד מרחק קוסינוס
   - מקסימום 20 איטרציות
5. דגימת זוגות חוצי-אשכולות
   - עד 6 זוגות לריצה
   - דילוג על זוגות עובדות שכבר כוסו
6. עבור כל זוג:
   a. שליחה ל-LLM: "צור תובנה יצירתית משני האשכולות הללו"
   b. ניקוד איכות (1-5) דרך קריאת LLM נפרדת
   c. שמירה אם איכות >= סף
```

### מחזור חיי תובנה

```
domain.Insight {
    ID             int64
    ChatID         string
    UserID         string
    Content        string     // טקסט התובנה
    FactIDs        string     // מזהי עובדות מקור מופרדים בפסיקים
    Quality        int        // ניקוד איכות LLM 1-5
    AccessCount    int
    LastAccessedAt time.Time
    CreatedAt      time.Time
    ExpiresAt      *time.Time // ברירת מחדל: נוצר + 30 יום
}
```

- **נוצרת** על ידי המתזמן ברקע לאחר אשכולות וסינתזת LLM
- **מוצגת** בפרומפט המערכת של העוזר כאשר רלוונטית מבחינת הקשר (חיפוש היברידי)
- **מחוזקת** בעת גישה (מונה גישות וזמן גישה אחרון מתעדכנים)
- **מקודמת** דרך מיומנות `promote_insight` (מאריכה או מסירה תפוגה)
- **נדחית** דרך מיומנות `dismiss_insight` (מחיקה מיידית)
- **פגה** — משימת הניקוי רצה כל שעה ומסירה תובנות שעברו את `expires_at`

### ניקוד איכות תובנות

לאחר יצירת תובנה, קריאת LLM שנייה מדרגת אותה:

```
System: "Rate the following insight on a scale of 1-5 for novelty and usefulness."
User: [טקסט התובנה]
Response: ספרה בודדת 1-5
```

אם `quality_threshold > 0` והניקוד מתחת לסף, התובנה נמחקת. זה מונע מתובנות באיכות נמוכה לעמוס על הזיכרון.

## הטבעות

### ספק ONNX

Iulita משתמש ב-runtime ONNX טהור ב-Go (`knights-analytics/hugot`) ליצירת הטבעות מקומית — ללא צורך בקריאות API חיצוניות.

- **מודל**: `KnightsAnalytics/all-MiniLM-L6-v2` — sentence transformer, 384 ממדים
- **Runtime**: Go טהור (ללא CGo, ללא ספריות משותפות)
- **בטיחות תהליכונים**: מוגן על ידי `sync.Mutex` (צינור hugot אינו thread-safe)
- **מטמון מודל**: מורד פעם אחת ל-`~/.local/share/iulita/models/`, נעשה בו שימוש חוזר

### אחסון וקטורי

הטבעות נשמרות כ-BLOBים בינאריים ב-SQLite:

- **קידוד**: כל `float32` → 4 בתים LittleEndian, ארוזים ל-`[]byte`
- **384 ממדים** → 1536 בתים לכל וקטור
- **טבלאות**: `fact_vectors` (fact_id PK), `insight_vectors` (insight_id PK)
- **מחיקת מפל**: הסרת עובדה/תובנה מסירה אוטומטית את הוקטור שלה

### מטמון הטבעות

טבלת `embedding_cache` מונעת חישוב מחדש של הטבעות לטקסטים זהים:

- **מפתח**: SHA-256 hash של טקסט הקלט
- **פינוי LRU**: שומר רק את N הרשומות שנגישו לאחרונה (ברירת מחדל 10,000)
- **בשימוש על ידי**: עטיפת `CachedEmbeddingProvider` סביב ONNX

### אלגוריתם חיפוש היברידי

```python
# פסאודו-קוד
def hybrid_search(query, user_id, limit):
    # 1. תוצאות FTS5 (דגימת יתר)
    fts_results = FTS_MATCH(query, limit * 2)
    fts_scores = {r.id: 1 - i/(len+1) for i, r in enumerate(fts_results)}

    # 2. דמיון וקטורי
    query_vec = onnx.embed(query)
    all_vecs = load_all_vectors(user_id)
    vec_scores = {id: cosine_similarity(query_vec, vec) for id, vec in all_vecs}

    # 3. שילוב
    all_ids = set(fts_scores) | set(vec_scores)
    combined = {}
    for id in all_ids:
        fts = fts_scores.get(id, 0)
        vec = vec_scores.get(id, 0)
        combined[id] = (1 - vectorWeight) * fts + vectorWeight * vec

    # 4. Top-N
    return sorted(combined, key=combined.get, reverse=True)[:limit]
```

**הגדרה**: `skills.memory.vector_weight` (ברירת מחדל 0, FTS בלבד). הגדר ל-0.3-0.5 לחיפוש היברידי.

## זיכרון בלולאת העוזר

כל הודעה מפעילה הזרקת זיכרון לפרומפט המערכת:

1. **עובדות אחרונות** (עד 20): נטענות מ-DB, דעיכה + MMR מיושמים, מעוצבות כ-`## Remembered Facts`
2. **תובנות רלוונטיות** (עד 5): חיפוש היברידי באמצעות טקסט ההודעה, מעוצבות כ-`## Insights`
3. **פרופיל משתמש** (tech facts): מטא-דאטה התנהגותי מקובץ לפי קטגוריה, מעוצב כ-`## User Profile`
4. **הנחיית משתמש**: הוראה מותאמת אישית קבועה, מעוצבת כ-`## User Directives`

הקשר זה מופיע ב**פרומפט המערכת הדינמי** (לכל הודעה, לא נשמר במטמון Claude).

## ייצוא / ייבוא זיכרון

### ייצוא

```go
memory.ExportFacts(ctx, store, chatID) // → מחרוזת Markdown
memory.ExportAllFacts(ctx, store, dir) // → קובץ .md אחד לכל שיחה
```

פורמט:
```markdown
## Fact 42
The user prefers dark mode in all IDEs.

## Fact 43
User's favorite programming language is Go.
```

### ייבוא

```go
memory.ImportFacts(ctx, store, chatID, markdownContent)
```

מפרש את ה-Markdown, יוצר עובדות חדשות (מזהים מקוריים נמחקים — מזהים חדשים מוקצים על ידי autoincrement של SQLite). כל עובדה מיובאת מוטבעת אוטומטית.

## הפניית הגדרות

| פרמטר | ברירת מחדל | תיאור |
|-----------|---------|-------------|
| `skills.memory.half_life_days` | 30 | זמן מחצית חיים של דעיכה זמנית; 0 = מושבת |
| `skills.memory.mmr_lambda` | 0 | גיוון MMR (0 = מושבת, 0.7 מומלץ) |
| `skills.memory.vector_weight` | 0 | שילוב חיפוש היברידי (0 = FTS בלבד, 0.5 = מאוזן) |
| `skills.insights.min_facts` | 20 | מספר עובדות מינימלי להפעלת יצירת תובנות |
| `skills.insights.max_pairs` | 6 | מקסימום זוגות חוצי-אשכולות לכל ריצת יצירה |
| `skills.insights.ttl` | 720h | TTL תפוגת תובנה (30 יום) |
| `skills.insights.interval` | 24h | תדירות יצירת תובנות |
| `skills.insights.quality_threshold` | 0 | ניקוד איכות מינימלי (0 = קבל הכל) |
| `embedding.model` | `KnightsAnalytics/all-MiniLM-L6-v2` | שם מודל ONNX |
| `embedding.enabled` | true | הפעל הטבעות ONNX |
