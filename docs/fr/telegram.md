# Telegram

Le [guide complet Telegram (EN)](../en/telegram.md) décrit les changements du code non encore publiés : sujets privés, cartes du jour et météo, alias, citations et callbacks personnels.

Configurez le jeton et `telegram.allowed_ids`, puis activez les sujets dans BotFather. Un message ordinaire à la racine crée un sujet ; `/topic` liste les sujets connus. `/clear` et `/compact` concernent le contexte courant. `/topic pause` suspend Iulita sans fermer le sujet ; `/topic delete` demande confirmation et annule le travail local lié, sans supprimer les tâches externes.

Utilisez `/day`, `/day text`, `/weather London 4` ou `/weather text London 4`. `telegram.day_widgets` et `telegram.day_actions` sont activés par défaut, y compris après mise à jour sans ces clés ; false explicite est conservé. Ces réglages nécessitent un redémarrage. Les actions demandent la confirmation du propriétaire.

Les réponses sont mises en mémoire tampon jusqu'au contrôle final de confidentialité. Aucun fragment non vérifié n'est publié. L'échec d'un envoi privé sensible ne déclenche pas un envoi ordinaire de ces données. Le regroupement sépare utilisateurs et sujets. Les widgets de production restent privés ; inline/shared nécessitent le serveur de test Telegram. Voir le guide EN pour la voix, les citations rich, la disponibilité et la récupération.

![English previews of day, weather and shared-result cards](/screenshots/telegram/card-family-en.png)

*Illustrative browser previews of production templates with sample English data, not native Telegram captures. Shared-result widgets require Telegram's test server.*
