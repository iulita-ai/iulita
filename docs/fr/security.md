# Securite

## Authentification

### JWT

- **Algorithme** : HMAC-SHA256
- **TTL du jeton d'acces** : 24 heures
- **TTL du jeton de rafraichissement** : 7 jours
- **Claims** : `user_id`, `username`, `role`
- **Secret** : hex de 32 octets genere automatiquement si non configure
- **Stockage** : trousseau (macOS Keychain / Linux SecretService) ou env `IULITA_JWT_SECRET`

### Mots de passe

- **Hachage** : bcrypt avec cout par defaut
- **Initialisation** : le premier utilisateur recoit un mot de passe aleatoire avec `MustChangePass: true`
- **Tableau de bord** : endpoint de changement de mot de passe a `POST /api/auth/change-password`

### Middleware

1. `FiberMiddleware` — valide `Authorization: Bearer <token>`, stocke les claims dans les locaux fiber
2. `AdminOnly` — verifie `role == admin`, retourne 403 si non

## Gestion des secrets

### Couches de stockage

| Secret | Variable d'environnement | Trousseau | Repli fichier |
|--------|--------------------------|-----------|---------------|
| Cle API Claude | `IULITA_CLAUDE_API_KEY` | `claude-api-key` | — |
| Jeton Telegram | `IULITA_TELEGRAM_TOKEN` | `telegram-token` | — |
| Secret JWT | `IULITA_JWT_SECRET` | `jwt-secret` | generation auto |
| Cle de chiffrement config | `IULITA_CONFIG_KEY` | `config-encryption-key` | fichier `encryption.key` |

**Ordre de resolution** : variable d'environnement → trousseau → repli fichier → generation automatique.

### Chiffrement de la configuration (AES-256-GCM)

Les surcharges de configuration en base de donnees a l'execution peuvent etre chiffrees :

- **Algorithme** : AES-256-GCM (chiffrement authentifie)
- **Nonce** : 12 octets, genere aleatoirement par chiffrement
- **Format** : `base64(nonce ‖ texte chiffre)`
- **Chiffrement automatique** : les cles declarees comme `secret_keys` dans les manifestes SKILL.md
- **Securite API** : le tableau de bord ne retourne jamais les valeurs dechiffrees pour les cles secretes
- **Rejet des espaces reserves** : les valeurs `"***"` ou vides sont rejetees pour les cles secretes

## Protection SSRF

Toutes les requetes HTTP sortantes (web fetch, recherche web, competences externes) passent par la protection SSRF.

### Plages d'IP bloquees

| Plage | Type |
|-------|------|
| `10.0.0.0/8` | Prive RFC1918 |
| `172.16.0.0/12` | Prive RFC1918 |
| `192.168.0.0/16` | Prive RFC1918 |
| `100.64.0.0/10` | NAT de qualite operateur (RFC 6598) |
| `fc00::/7` | IPv6 Unique Local |
| `127.0.0.0/8` | Loopback |
| `::1/128` | Loopback IPv6 |
| `169.254.0.0/16` | Lien local |
| `fe80::/10` | Lien local IPv6 |
| Plages multicast | Toutes |

Les adresses IPv6 mappees IPv4 sont normalisees en IPv4 avant verification.

### Protection double couche (sans proxy)

**Couche 1 — DNS pre-vol** : avant la connexion, toutes les IPs du nom d'hote sont resolues. Si une IP est privee, la connexion est rejetee.

**Couche 2 — Controle a la connexion** : une fonction `net.Dialer.Control` verifie l'IP reellement resolue au moment de la connexion. Cela detecte les **attaques de rebinding DNS** ou un nom d'hote se resout vers une IP publique lors du pre-vol mais se re-lie a une IP privee avant la connexion reelle.

### Chemin proxy

Lorsqu'un proxy est configure (`proxy.url`), l'approche basee sur le dialer ne peut pas etre utilisee (le proxy lui-meme peut avoir une IP privee dans les clusters Kubernetes). A la place :

- `ssrfTransport.RoundTrip` effectue uniquement la verification pre-vol au niveau de l'URL
- La connexion proxy vers les IPs privees est autorisee (intentionnel pour les proxies internes au cluster)
- Les URLs cibles vers les IPs privees sont toujours bloquees

### Detection de proxy actif

`isProxyActive()` appelle reellement la fonction proxy avec une requete de test (pas juste `Proxy != nil`), car `http.DefaultTransport` a toujours `Proxy = ProxyFromEnvironment` defini.

## Niveaux d'approbation des outils

| Niveau | Comportement | Competences |
|--------|-------------|-------------|
| `ApprovalAuto` | Executer immediatement | La plupart des competences (par defaut) |
| `ApprovalPrompt` | L'utilisateur doit confirmer | Executeur Docker |
| `ApprovalManual` | L'administrateur doit confirmer | Execution shell |

### Flux

1. La competence declare son niveau via l'interface `ApprovalDeclarer`
2. Avant l'execution, l'assistant verifie `registry.ApprovalLevelFor(toolName)`
3. Pour `Prompt`/`Manual` : stocke l'appel d'outil en attente dans `approvalStore`
4. Envoie l'invite de confirmation a l'utilisateur
5. Retourne « en attente d'approbation » au LLM (non bloquant)
6. Le message suivant de l'utilisateur est verifie contre le vocabulaire d'approbation sensible a la locale
7. Si approuve : execute l'appel d'outil stocke, retourne le resultat
8. Si rejete : retourne « annule »

### Vocabulaire sensible a la locale

Les mots d'approbation sont definis dans les 6 catalogues de langue et incluent l'anglais en repli :

```
# Affirmatif russe :
да, д, ок, подтвердить, подтверждаю, yes, y, ok, confirm

# Negatif hebreu :
לא, ביטול, בטל, no, n, cancel
```

## Securite Telegram

- **Liste blanche d'utilisateurs** : `telegram.allowed_ids` restreint qui peut discuter avec le bot
- **Liste blanche vide** : autorise tous les utilisateurs (avertissement journalise)
- **Limitation de debit** : limiteur de debit a fenetre glissante par chat

## Securite de l'execution shell

La competence `shell_exec` a la securite la plus stricte :

- **Niveau d'approbation** : `ApprovalManual` (confirmation administrateur requise)
- **Liste blanche uniquement** : seuls les executables dans `AllowedBins` peuvent s'executer
- **Chemins interdits** : liste configurable de chemins qui ne peuvent pas apparaitre dans les arguments
- **Traversee de chemin** : `..` dans les arguments est rejete
- **Limite de sortie** : max 16 Ko
- **Repertoire de travail** : `os.TempDir()` (pas le repertoire du projet)

## Limitation de debit

### Limiteur de debit par chat

Fenetre glissante : suit les horodatages par `chatID`. Si le nombre de messages dans la `window` depasse le `rate`, le message est rejete.

### Limiteur d'actions global

Fenetre fixe : nombre total d'actions LLM/outils par heure a travers tous les chats. Reinitialisation automatique a la limite de la fenetre.

## Suivi des couts

- **En memoire** : cout quotidien suivi avec mutex, reinitialisation automatique a la limite du jour
- **Persistant** : `IncrementUsageWithCost` sauvegarde dans la table `usage_stats`
- **Limite quotidienne** : `cost.daily_limit_usd` (0 = illimite)
- **Tarification par modele** : `config.ModelPrice{InputPerMillion, OutputPerMillion}`

## Securite CI/CD

- **Hook pre-commit** : bloque les secrets via [gitleaks](https://github.com/gitleaks/gitleaks)
- **CI** : l'action gitleaks scanne tous les commits
- **CodeQL** : requetes de securite etendues pour Go et JavaScript/TypeScript (lorsque le depot est public)
- **Dependances** : alertes Dependabot (activer dans les parametres GitHub)

## Securite des competences externes

- **Validation du slug** : `^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$` — empeche la traversee de chemin
- **Verification de la somme de controle** : SHA-256 pour les telechargements distants
- **Validation de l'isolation** : les competences doivent declarer un niveau d'isolation, verifie contre les drapeaux de configuration
- **Detection de code** : rejette les competences avec des fichiers de code sauf si correctement isolees
- **Scan d'injection de prompt** : avertit sur les motifs suspects dans le corps de la competence
- **Taille max d'archive** : configurable (defaut 50 Mo pour ClawhHub)
