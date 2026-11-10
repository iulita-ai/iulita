# Telegram

[המדריך המלא ל-Telegram באנגלית](../en/telegram.md) מתאר שינויי קוד שטרם פורסמו: נושאים פרטיים, כרטיסי יום ומזג אוויר, שמות נוספים, ציטוטים ותשובות אישיות ללחיצות.

הגדירו אסימון ו-`telegram.allowed_ids` והפעילו נושאים ב-BotFather. הודעה רגילה בצ׳אט הראשי יוצרת נושא; `/topic` מציגה נושאים מוכרים. `/clear` ו-`/compact` פועלות על ההקשר הנוכחי. `/topic pause` משהה את Iulita בלי לסגור את הנושא; `/topic delete` דורשת אישור ומנקה עבודה מקומית קשורה בלי למחוק משימות חיצוניות.

השתמשו ב-`/day`, `/day text`, `/weather London 4` או `/weather text London 4`. ההגדרות `telegram.day_widgets` ו-`telegram.day_actions` פעילות כברירת מחדל, גם לאחר עדכון תצורה ללא המפתחות. false מפורש נשמר; שינויים דורשים הפעלה מחדש ופעולות דורשות אישור בעלים.

התשובה נשמרת במאגר עד לבדיקת פרטיות סופית; לא נחשפים מקטעים שלא נבדקו. כשל בשליחה פרטית של מידע רגיש אינו מוביל לשליחה רגילה שלו. איחוד הודעות מפריד בין משתמשים ונושאים. בייצור, widgets זמינים רק בצ׳אט פרטי; inline/shared דורשים את שרת הבדיקה של Telegram. פרטי קול, ציטוטים, זמינות ושחזור במדריך האנגלי.

![English previews of day, weather and shared-result cards](/screenshots/telegram/card-family-en.png)

*Illustrative browser previews of production templates with sample English data, not native Telegram captures. Shared-result widgets require Telegram's test server.*
