package blueprint

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"project-synapse/backend/internal/llm"
)

// Extractor decomposes a feature description into entities + actions. It uses
// the LLM when configured, falling back to a deterministic heuristic so the
// engine is fully functional offline.
type Extractor struct {
	Chat llm.ChatClient // nil => heuristic only
}

const intentSystemPrompt = `You are a software architect. Decompose the user's feature/PRD description into its core technical intents.

Respond with a SINGLE JSON object and nothing else, exactly:
{
  "target_entities": [ { "name": "snake_case_noun", "description": "short" } ],
  "target_actions":  [ { "name": "snake_case_verb", "description": "short" } ]
}

Rules:
- Entities are the data/domain nouns (e.g. users, transactions, plans, quotas).
- Actions are the operations (e.g. subscribe, modify_quota, check_balance).
- Use concise snake_case names. Output valid JSON only, no prose or code fences.`

// Extract returns the intent breakdown for a description.
func (e *Extractor) Extract(ctx context.Context, description string) IntentBreakdown {
	if e.Chat != nil {
		if ib, err := e.llmExtract(ctx, description); err == nil && (len(ib.Entities) > 0 || len(ib.Actions) > 0) {
			return ib
		}
	}
	return heuristicExtract(description)
}

type llmIntentPayload struct {
	TargetEntities []Intent `json:"target_entities"`
	TargetActions  []Intent `json:"target_actions"`
}

func (e *Extractor) llmExtract(ctx context.Context, description string) (IntentBreakdown, error) {
	raw, err := e.Chat.Complete(ctx, intentSystemPrompt, "Feature description:\n"+description)
	if err != nil {
		return IntentBreakdown{}, err
	}
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return IntentBreakdown{}, errNoJSON
	}
	var p llmIntentPayload
	if err := json.Unmarshal([]byte(raw[start:end+1]), &p); err != nil {
		return IntentBreakdown{}, err
	}
	return IntentBreakdown{Entities: p.TargetEntities, Actions: p.TargetActions}, nil
}

var errNoJSON = jsonError("no JSON object in LLM response")

type jsonError string

func (e jsonError) Error() string { return string(e) }

// --- Heuristic fallback -----------------------------------------------------

var wordRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_]+`)

// actionVerbs is a lexicon of operation words. A token (or its de-inflected
// base) in this set is classified as an action; other significant words are
// treated as entities.
var actionVerbs = map[string]bool{
	"subscribe": true, "unsubscribe": true, "modify": true, "create": true,
	"update": true, "delete": true, "remove": true, "fetch": true, "get": true,
	"list": true, "check": true, "manage": true, "assign": true, "track": true,
	"save": true, "send": true, "validate": true, "authenticate": true,
	"authorize": true, "generate": true, "calculate": true, "process": true,
	"handle": true, "schedule": true, "notify": true, "upload": true,
	"download": true, "search": true, "filter": true, "sort": true,
	"login": true, "logout": true, "register": true, "cancel": true,
	"renew": true, "refund": true, "charge": true, "bill": true, "invoice": true,
	"reset": true, "verify": true, "approve": true, "reject": true, "import": true,
	"export": true, "sync": true, "publish": true, "archive": true,
}

var stopwords = map[string]bool{
	"add": true, "the": true, "and": true, "with": true, "for": true, "their": true,
	"can": true, "new": true, "feature": true, "module": true, "system": true,
	"support": true, "also": true, "that": true, "this": true, "where": true,
	"when": true, "into": true, "from": true, "your": true, "our": true,
	"should": true, "must": true, "able": true, "allow": true, "user's": true,
	"each": true, "any": true, "all": true, "some": true, "via": true, "per": true,
	"build": true, "implement": true, "want": true, "need": true, "would": true,
	"like": true, "let": true, "lets": true, "them": true, "they": true,
}

// deInflect strips common verb/noun inflections to a base form.
func deInflect(w string) string {
	switch {
	case strings.HasSuffix(w, "ing") && len(w) > 5:
		base := w[:len(w)-3]
		if !actionVerbs[base] && actionVerbs[base+"e"] {
			return base + "e" // billing -> bill is in lexicon; subscribing -> subscribe
		}
		return base
	case strings.HasSuffix(w, "ies") && len(w) > 4:
		return w[:len(w)-3] + "y"
	case strings.HasSuffix(w, "es") && len(w) > 4:
		return w[:len(w)-2]
	case strings.HasSuffix(w, "s") && len(w) > 3:
		return w[:len(w)-1]
	default:
		return w
	}
}

func heuristicExtract(description string) IntentBreakdown {
	seenEnt := map[string]bool{}
	seenAct := map[string]bool{}
	var entities, actions []Intent

	for _, raw := range wordRe.FindAllString(description, -1) {
		w := strings.ToLower(raw)
		if len(w) < 3 || stopwords[w] {
			continue
		}
		base := deInflect(w)
		if actionVerbs[w] || actionVerbs[base] {
			key := base
			if actionVerbs[w] {
				key = w
			}
			if !seenAct[key] {
				seenAct[key] = true
				actions = append(actions, Intent{Name: key})
			}
			continue
		}
		// Otherwise treat as an entity (use the de-pluralised base).
		if !seenEnt[base] {
			seenEnt[base] = true
			entities = append(entities, Intent{Name: base})
		}
	}

	return IntentBreakdown{Entities: entities, Actions: actions}
}
