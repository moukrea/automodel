// Package jev calls the OpenRouter Decisions API (TypeSafe Jev) with the
// routing questions generated from the catalog.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/moukrea/automodel/internal/catalog"
)

type Client struct {
	URL    string
	APIKey string
	HTTP   *http.Client
}

type Request struct {
	Model     string              `json:"model"`
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
	SessionID string              `json:"session_id,omitempty"`
}

// Question is a Choice (criteria: option -> description), a Score
// (criteria: ordered level descriptions) or a Noul (criteria: true/false).
type Question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Answer holds any answer type: Choice (Choice), Score (Score, keyed
// probabilities "0".."n"), Noul (Noul, the yes-probability).
type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Score         float64            `json:"score"`
	Noul          *float64           `json:"noul"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type Response struct {
	ID      string            `json:"id"`
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   struct {
		InputTokens int     `json:"input_tokens"`
		Cost        float64 `json:"cost"`
	} `json:"usage"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// QuestionName is the key of the single question we ask.
const QuestionName = "tier"

var instructions = map[string]string{
	catalog.ScopeMain:     "Which reasoning tier should handle the rest of this Claude Code session?",
	catalog.ScopeSubagent: "Which model and reasoning tier should handle this Claude Code subagent task?",
}

// TierQuestion builds the v1 Choice from the catalog: one option per tier.
// Kept for `automodel eval --format choice`.
func TierQuestion(c *catalog.Catalog, scope string) Question {
	crit := map[string]string{}
	for _, t := range c.ScoredTiers(scope) {
		crit[t.ID] = t.Criteria
	}
	return Question{Type: "choice", Instructions: instructions[scope], Criteria: crit}
}

// Question IDs of a routing request.
const (
	QLevel       = "level"
	QRelation    = "relation"
	QOffer       = "offer"
	QRework      = "rework"
	QExplicitPfx = "explicit_"
	QTierPfx     = "tier_"
	QModePfx     = "mode_"
)

// DefaultLevel is the Score question's built-in instructions, per scope
// (the catalog's questions.level overrides them).
var DefaultLevel = map[string]string{
	catalog.ScopeMain:     "How much reasoning does the work that the new prompt starts need, judging by what that work actually involves (a short prompt can approve a large job)? When the prompt hands work to subagents, judge only what this session does itself (launching them, then relaying or merging their reports): each subagent gets its own level.",
	catalog.ScopeSubagent: "How much capability and reasoning does this subagent task need?",
}

// Ask says which of the optional questions a request carries.
type Ask struct {
	// Relation: how the prompt relates to the work in progress (main
	// session, once there is work in progress). Resume offers going back
	// to the work a detour paused (once there is one).
	Relation, Resume bool
	// Offer: whether the assistant offered one more thing for the detour
	// that the go-ahead accepts (a bare go-ahead after a detour whose
	// paused work needs more).
	Offer bool
	// Rework: whether the prompt says the assistant's last work was left
	// undone, wrong or botched (a follow-up while the turn runs above the
	// work's level).
	Rework bool
	// Explicit: every request the prompt could make, asked as one Choice
	// per kind (Jev tells a request from a mention, and which one).
	Explicit []Explicit
}

// Explicit is a request a prompt's words may make: an effort ("more": more
// thinking), a mode ("off": the mode refused), a model.
type Explicit struct {
	Kind  string // effort | mode | model
	Value string // low..max or more; the mode's ID or off; the model's catalog key
	Label string // what the question calls it: the effort, the mode's ID, the model's label
}

// Kinds and special values of explicit requests.
const (
	ExplicitEffort = "effort"
	ExplicitMode   = "mode"
	ExplicitModel  = "model"
	ExplicitMore   = "more"
	ExplicitOff    = "off"
	// ExplicitNone is the option of a request Choice for a prompt that asks
	// for nothing of that kind.
	ExplicitNone = "none"
)

// ID is the request's question ID: explicit_effort_xhigh, explicit_mode_off...
func (x Explicit) ID() string { return QExplicitPfx + x.Kind + "_" + x.Value }

// Questions builds the routing questions of a scope: a Score over the tiers
// (they are ordered, so a Score fits better than a Choice), one Noul per
// mode and per asked tier, and what a asks for: a Choice on the prompt's
// relation to the work in progress, a Choice per kind of explicit request.
// It returns the tier IDs in level order.
func Questions(c *catalog.Catalog, scope string, a Ask) (map[string]Question, []string) {
	var levels, ids []string
	for _, t := range c.ScoredTiers(scope) {
		levels, ids = append(levels, t.Criteria), append(ids, t.ID)
	}
	level := DefaultLevel[scope]
	if v := c.Questions.Level[scope]; v != "" {
		level = v
	}
	qs := map[string]Question{QLevel: {Type: "score", Instructions: level, Criteria: levels}}
	for _, t := range c.TiersByRank(scope) {
		if t.Asked() {
			qs[QTierPfx+t.ID] = Question{Type: "noul", Instructions: t.Question, Criteria: map[string]string{"true": t.Criteria, "false": t.No}}
		}
	}
	for _, m := range c.ModesFor(scope) {
		qs[QModePfx+m.ID] = Question{Type: "noul", Instructions: m.Question, Criteria: map[string]string{"true": m.Yes, "false": m.No}}
	}
	if a.Relation && scope == catalog.ScopeMain {
		qs[QRelation] = RelationQuestion(c, a.Resume)
	}
	if a.Offer && scope == catalog.ScopeMain {
		qs[QOffer] = OfferQuestion(c)
	}
	if a.Rework && scope == catalog.ScopeMain {
		qs[QRework] = ReworkQuestion(c)
	}
	for id, q := range ExplicitQuestions(c, a.Explicit) {
		qs[id] = q
	}
	return qs, ids
}

// RelationQuestion is the Choice on how the prompt relates to the work in
// progress: the catalog's wording (questions.relation) over the built-in
// one, each option an object {what, not_for, examples}. The resume option
// is only offered when there is paused work to go back to.
func RelationQuestion(c *catalog.Catalog, resume bool) Question {
	q, opts := DefaultRelation.Question, map[string]*catalog.Option{}
	r := c.Questions.Relation
	if r != nil && r.Question != "" {
		q = r.Question
	}
	for _, id := range catalog.Relations {
		if id == catalog.RelationResume && !resume {
			continue
		}
		opts[id] = DefaultRelation.Options[id]
		if r != nil && r.Options[id] != nil && r.Options[id].What != "" {
			opts[id] = r.Options[id]
		}
	}
	return Question{Type: "choice", Instructions: q, Criteria: opts}
}

// OfferQuestion is the yes/no on a go-ahead after a detour: the catalog's
// wording (questions.offer) over the built-in one.
func OfferQuestion(c *catalog.Catalog) Question {
	w := DefaultOffer
	if o := c.Questions.Offer; o != nil && o.Question != "" {
		w = *o
	}
	return Question{Type: "noul", Instructions: w.Question, Criteria: map[string]string{"true": w.Yes, "false": w.No}}
}

// ReworkQuestion is the yes/no on a complaint that the last work was left
// undone or botched: the catalog's wording (questions.rework) over the
// built-in one.
func ReworkQuestion(c *catalog.Catalog) Question {
	w := DefaultRework
	if o := c.Questions.Rework; o != nil && o.Question != "" {
		w = *o
	}
	return Question{Type: "noul", Instructions: w.Question, Criteria: map[string]string{"true": w.Yes, "false": w.No}}
}

// ExplicitQuestions are the Choices on what a prompt asks of the assistant
// itself, one per kind of request in xs (QExplicitPfx + kind): the effort
// or more thinking, the workflow mode or its refusal, the model. Each
// option is a request (its value), plus none. Their options compete, so
// "passe en xhigh" reads xhigh, not high nor the mode as well.
func ExplicitQuestions(c *catalog.Catalog, xs []Explicit) map[string]Question {
	opts := map[string]map[string]*catalog.Option{}
	label := map[string]string{}
	for _, x := range xs {
		w := explicitWording(c, x.Kind)
		if opts[x.Kind] == nil {
			opts[x.Kind] = map[string]*catalog.Option{ExplicitNone: w.Options[ExplicitNone]}
		}
		key := x.Value
		switch {
		case x.Kind == ExplicitMode && x.Value != ExplicitOff:
			key = "on"
		case x.Kind == ExplicitModel:
			key = "model"
		}
		if x.Kind == ExplicitMode {
			label[x.Kind] = x.Label
		}
		opts[x.Kind][x.Value] = fillOption(w.Options[key], x.Label)
	}
	qs := map[string]Question{}
	for kind, o := range opts {
		if v := label[kind]; v != "" {
			o[ExplicitNone] = fillOption(o[ExplicitNone], v)
		}
		qs[QExplicitPfx+kind] = Question{Type: "choice", Instructions: strings.ReplaceAll(explicitWording(c, kind).Question, "{v}", label[kind]), Criteria: o}
	}
	return qs
}

// explicitWording is the Choice wording of a kind of request: the
// catalog's (questions.explicit_<kind>) over the built-in one, option by
// option.
func explicitWording(c *catalog.Catalog, kind string) catalog.Relation {
	w, r := DefaultExplicitEffort, c.Questions.ExplicitEffort
	switch kind {
	case ExplicitMode:
		w, r = DefaultExplicitMode, c.Questions.ExplicitMode
	case ExplicitModel:
		w, r = DefaultExplicitModel, c.Questions.ExplicitModel
	}
	if r == nil {
		return w
	}
	out := catalog.Relation{Question: w.Question, Options: map[string]*catalog.Option{}}
	if r.Question != "" {
		out.Question = r.Question
	}
	for id, o := range w.Options {
		out.Options[id] = o
		if ro := r.Options[id]; ro != nil && ro.What != "" {
			out.Options[id] = ro
		}
	}
	return out
}

// fillOption is o with {v} replaced by v.
func fillOption(o *catalog.Option, v string) *catalog.Option {
	if o == nil {
		return nil
	}
	f := &catalog.Option{What: strings.ReplaceAll(o.What, "{v}", v), NotFor: strings.ReplaceAll(o.NotFor, "{v}", v)}
	for _, e := range o.Examples {
		f.Examples = append(f.Examples, strings.ReplaceAll(e, "{v}", v))
	}
	return f
}

// ExplicitProbs reads the answers to the explicit-request questions, keyed
// by request ID (explicit_effort_low, explicit_mode_off...): a Choice's
// options but none, or a yes/no per request (answers saved before the
// Choices).
func ExplicitProbs(ans map[string]Answer) map[string]float64 {
	var out map[string]float64
	for id, a := range ans {
		if !strings.HasPrefix(id, QExplicitPfx) {
			continue
		}
		if out == nil {
			out = map[string]float64{}
		}
		if a.Noul != nil {
			out[id] = *a.Noul
			continue
		}
		for opt, p := range a.Probabilities {
			if opt != ExplicitNone {
				out[id+"_"+opt] = p
			}
		}
	}
	return out
}

// DefaultRelation and the DefaultExplicit Choices are the built-in
// wordings (the catalog's questions.relation and questions.explicit_*
// override them).
var (
	DefaultRelation = catalog.Relation{
		Question: "How does the new prompt `task` relate to the work in progress (started by `work_in_progress.goal`, carried on in `recent_prompts`, last reported in `last_assistant`; `work_in_progress.done` once it was wrapped up)?",
		Options: map[string]*catalog.Option{
			catalog.RelationContinue: {
				What:     "Tells the assistant to go ahead with or keep going on the work in progress (`work_in_progress.goal`) as it stands, also after a break, possibly at another effort, on another model or without the parallel agents, or to carry out what it just proposed or offered for that same work (possibly picking one of its options), and asks for nothing more. A question or a remark that comes with the go-ahead, even an unrelated one, doesn't change that: the work goes on.",
				NotFor:   "Going back to the paused work (`paused_work.goal`), including a go-ahead to the assistant's offer to get back to it ('yes' to 'Shall I get back to the migration?'), and a go-ahead once the detour is finished when `last_assistant` offers nothing more of it and only closes on a question such as 'Anything else?' or 'Autre chose ?' (resume); a go-ahead that also adds or changes something (extend); an acknowledgement once the work is done (`work_in_progress.done`) and nothing is left to go on with, such as 'looks good', 'ok, thanks' or 'merci' (aside); a go-ahead to a wrap-up step the assistant proposed once the work is done, such as committing, pushing, opening the PR or writing the changelog entry ('yes' to 'Want me to open the PR?') (wrap_up).",
				Examples: []string{"yes", "go", "continue", "ok ship it", "ok, do what you proposed", "go with option 2", "resume, the limits are reset", "out of curiosity, why is the status line orange? anyway, carry on", "vas-y", "oui, continue", "oui, fais ce que tu proposes", "c'est bon, on y va"},
			},
			catalog.RelationExtend: {
				What:     "Adds to, constrains or corrects the work in progress, which stays the same piece of work: another case or input to handle (one more file or module for the audit or the review in progress), a test for it or for behaviour it added or changed, an option or a flag for what it built, a requirement, a different approach, or a step it calls for, such as repairing what the problem it fixes left behind (the data, the orders or the charges it got wrong), or fixing a regression or a side effect the work caused ('since the refactor, the dialog text is cut off: can you check that too?'), or feedback on how the assistant goes about it (its reporting, its pace, its method, what it spends its time on), which changes how the work goes on, or a step the assistant left out or forgot, also a standing one set earlier in the session that the recent prompts no longer show, such as releasing after merges or keeping the docs in sync ('lots of merges but no release: did you forget?').",
				NotFor:   "Work that stands on its own without the work in progress, including the same change repeated on another target (new_task).",
				Examples: []string{"also add a test for that", "and make it configurable", "but keep the old flag working", "no, use a channel instead", "and write the script that fixes the rows it corrupted", "since your change the export button stays greyed out, can you look?", "ajoute aussi un log quand ça échoue", "mais garde l'ancienne API", "non, fais plutôt une migration", "depuis ta modif le tri ne marche plus sur mobile, tu regardes ?", "your status notes are too vague, and stop spending so long on screenshots nobody looks at", "le suivi est trop maigre, et tu perds du temps sur des détails que je ne regarde pas", "I see merges but no new release, did you forget?", "t'as oublié de mettre la doc à jour ?"},
			},
			catalog.RelationInform: {
				What:     "Only gives a fact, a preference or an answer the work in progress needs, and asks for no new work: about the work itself or how a part of it may be run when nothing is to be changed for it (the effort or the model a subagent can run at for one step), or about its environment, resources, schedule or people (which machines are free and until when, who shares them, who owns what, when someone is away).",
				NotFor:   "A message that also asks for a change or a check (extend), including setting something in a file or a config, even a subagent's effort (new_task or extend).",
				Examples: []string{"FYI it only fails on ARM", "env vars win", "camelCase", "the snapshot-refresh subagent can stay at low effort", "the staging cluster only has 4 GPUs free until noon", "FYI the build farm is shared with the mobile team this week", "heads-up: Sam, who reviews the billing code, is off until Monday", "c'est la v2 de l'API", "la clé est dans le .env", "le runner de CI n'est dispo que jusqu'à 17h", "au fait, Léa est en congés cette semaine et c'est elle qui valide les déploiements"},
			},
			catalog.RelationSideQuestion: {
				What:     "Asks a question or a quick check about the work in progress itself while it is still pending (steps remain, it is running, or a proposal awaits an answer), and only needs an answer: its progress or status, a detail of its code, a choice it made, a doubt, a check of its result, or which model or mode would suit it.",
				NotFor:   "A question about automodel's routing, such as why this session or a subagent got, kept or changed its effort, its model or its mode, or what the status line shows (aside), unless routing is what the work in progress builds or tunes; a question about model prices, also compared on the work at hand (aside), not about which model would suit it; a question unrelated to the work in progress (aside); a question that also tells the assistant to carry on with the work, such as 'just curious, keep going' (continue); a question that starts real work of its own, such as an investigation or a change (new_task); a question about work that is finished, which only recalls or explains it (wrap_up).",
				Examples: []string{"is CI green yet?", "why did you pick a mutex there?", "does that cover the retry path too?", "which Go version do we target again?", "t'en es où ?", "le build passe ?", "pourquoi un mutex et pas un channel ?", "anything for me to look at yet, or is it idle?", "il y a quelque chose à regarder, ou ça dort ?"},
			},
			catalog.RelationAside: {
				What:     "A question or a remark that is not about the work in progress itself and starts no work of its own: general knowledge, a command or a flag, another topic, news, a comment in passing or a thank-you, or a question about automodel's routing (why this session or a subagent got, kept or changed its effort, its model or its mode, or what the status line shows), or about model prices, also compared on the work at hand.",
				NotFor:   "A question about the work in progress, its code, its choices or its result, including which model or mode would suit it (side_question), and routing questions when routing is what the work in progress builds or tunes; a prompt that also tells the assistant to go on with the work, even after a question ('just curious, carry on'), or asks for an effort, a model or a mode for it (continue, extend); a fact or a constraint for the work, even about a subagent (the effort or the model it can run at) or a config, or about its environment, resources, schedule or people, such as which machines are free and until when or who shares them (inform, extend); any instruction to do or change something, even small or in a config or a tuning file (new_task or extend); feedback or a complaint about how the assistant does the work in progress, its reporting, its pace or its method (extend); a reproach that the assistant forgot or skipped a step of its work, even a standing one not in the recent prompts, such as releasing after merges or updating the docs (extend); a casual check on where the work stands, such as 'anything for me to look at, or is it idle?' (side_question); a question that reports something wrong or surprising, such as 'why does it show X when Y?' or an image of odd behaviour: it asks for an investigation, and most often a fix (new_task, or extend when it is about what the work builds).",
				Examples: []string{"unrelated: how do I list open ports on macOS?", "what does HTTP 409 mean again?", "Fable 5.1 is out, have you seen the benchmarks?", "why did the effort go up just now?", "au fait, c'est quoi la différence entre rebase et merge ?", "ça veut dire quoi idempotent, déjà ?", "pourquoi cette session tourne sur Opus ?", "haha, nice", "merci !"},
			},
			catalog.RelationResume: {
				What:     "Goes back to the paused work (`paused_work.goal`), which a detour set aside, now that the detour is done or dropped: by naming it, by accepting the assistant's offer to get back to it ('yes', 'oui' or 'go ahead' when `last_assistant` asks 'Shall I get back to the migration?'), or by a go-ahead or a question about what comes next once the detour is finished, also when `last_assistant` closes on a question that offers nothing more of the detour ('Anything else?', 'Autre chose ?').",
				NotFor:   "Going on with the detour itself, including a go-ahead to more of it that the assistant offered ('yes' to 'Want me to fix the two other typos too?') (continue); a wrap-up step of the detour the assistant offered ('yes' to 'Want me to push it?') (wrap_up); a piece of work neither of them is about (new_task).",
				Examples: []string{"back to the migration", "ok, now let's get back to the refactor", "continue the audit", "ok, and now?", "yes (to 'Done. Shall I get back to the migration?')", "reprends le refacto", "on revient à la migration", "bon, on reprend l'audit", "et maintenant ?", "oui (à « C'est fait. On reprend le refacto ? »)"},
			},
			catalog.RelationWrapUp: {
				What:     "Wraps up work that is finished: a summary or a recap, a commit message, a PR description, a push, a changelog entry, or a question that only recalls or explains the finished work (what changed, how it works, why it was done that way). A go-ahead to such a step the assistant proposed once the work is done ('yes' to 'Want me to push the branch and open the PR?') is a wrap-up too, also when other work waits, paused ('yes' to 'Committed. Want me to push it?' after a detour).",
				NotFor:   "Finishing or fixing the work itself (extend); a question while the work is still pending (side_question).",
				Examples: []string{"write the commit message", "summarize what you changed", "open the PR", "push it", "yes, push it and open the PR", "how does the new retry work, in two sentences?", "résume ce que tu as fait", "c'était quoi le problème, finalement ?", "fais le commit et pousse", "oui, vas-y pour la PR"},
			},
			catalog.RelationNewTask: {
				What:     "Starts a separate piece of work (a change, a fix, a feature, an investigation) that the work in progress doesn't include, even one that repeats its pattern on another target (another endpoint, page or module).",
				NotFor:   "More work on the work in progress itself, such as a test of what it added or one more file for the audit or the review it is, or a problem it caused (extend); carrying out what the assistant just proposed for it (continue); a question that starts no work (side_question, aside).",
				Examples: []string{"now rename the config loader", "next: design how to shard the job queue", "now the same retry logic for the email sender", "unrelated, but the login page is slow", "autre chose : mets à jour le README", "passons au module de facturation"},
			},
		},
	}
	DefaultOffer = catalog.Noul{
		Question: "The new prompt `task` is a go-ahead after a detour (`work_in_progress.goal`) that set bigger work aside (`paused_work.goal`). Does the assistant's last message (`last_assistant`) offer or ask to do one more specific thing for the detour itself, which that go-ahead accepts?",
		Yes:      "Near its end the message names one more specific thing it would do for the detour, and the go-ahead says yes to it: a wrap-up step of the detour ('Shall I open a PR for it?', 'Je pousse la branche ?') or more of it ('The same null check is missing in the export handler: want me to add it there too?', 'Je fais pareil dans le module d'import ?'), also when a remark follows the offer ('Shall I push the branch? CI takes about ten minutes.'), when the offer has no question mark ('dis-moi si je lance aussi le linter'), or when the go-ahead is only an acknowledgement ('ok', 'perfect', 'lgtm', 'super', 'top', 'nickel', 'parfait'): right after an offer, it accepts it.",
		No:       "The message names nothing more to do for the detour: it reports the detour done or where it stands and closes on a general question or a check that names no step of it ('Anything else?', 'Is that OK?', 'Can I go on?', 'Autre chose ?'); or it offers to go back to the paused work ('Shall I get back to the migration?', 'On reprend la migration ?'); or the go-ahead answers something else. The message decides, whatever the go-ahead's words.",
	}
	DefaultRework = catalog.Noul{
		Question: "Does the new prompt `task` say that what the assistant did last for the work in progress (`last_assistant`, `recent_prompts`) is left undone, incomplete, wrong or botched, and send it back to do it properly?",
		Yes:      "The prompt complains that the assistant's last work falls short and wants it done properly: parts it skipped or left out ('the edge cases still aren't handled', 'half the screens are missing'), a result that doesn't work or is wrong ('it still crashes on the second run', 'that's not what I asked for, redo it'), or work done carelessly or with too little effort ('you didn't even look at the logs', 'you have all the data, figure it out', 'c'est bâclé, refais-le correctement'), also angry or sarcastic.",
		No:       "Anything else: a new requirement, case or step the earlier work never covered, a change of mind or a preference ('make the button blue instead', 'let's use Postgres after all'), a regression or a side effect found later, a go-ahead, a question, praise or an acknowledgement, a remark about the assistant's pace or reports, a complaint about something other than the assistant's work (a tool, CI, a colleague, automodel's routing).",
	}
	DefaultExplicitEffort = catalog.Relation{
		Question: "Does the new prompt `task` ask the assistant itself to work at a given reasoning effort, or to think more than usual, for its own work (this prompt, a part of it, or the rest of the work in progress)? Pick the level the prompt names, more when it asks to think more without naming one, or none. The request can take any wording, in any language. Only when it names no level but a step from the current one ('one notch lower', 'un cran au-dessus') is it the level next to `current.effort`, in the order low, medium, high, xhigh, max.",
		Options: map[string]*catalog.Option{
			"none": {
				What:     "It asks for no effort of the assistant's own: most prompts. Also when it only talks about effort or thinking (a question, a mention, news, a complaint, a refusal), sets an effort for something else (subagents, workflow agents or a workflow stage, a config, a tuning or catalog entry, automodel's routing), quotes or tests a prompt that contains one, uses the ultrathink keyword (handled apart), or only asks for a mode with several agents or for a model.",
				NotFor:   "A request for the assistant's own effort, up or down, also as a cap, for a part of the work or with a reason ('low is enough for the changelog', 'dial it back to medium', 'think harder about it').",
				Examples: []string{"fix the failing test", "go through the payment module carefully and list every unchecked error", "look into the memory leak in depth this time", "automodel put this at low but it looks tricky to me", "why did it stay at xhigh?", "give the review agents low effort", "effort = \"medium\" in the stage config", "a test that the prompt \"fais-le en max\" pins nothing", "max retries is 3", "the hot path needs low latency", "use ultracode for the audit", "switch to Sonnet for this", "ultrathink: is the lease renewed under the lock?", "pas besoin de xhigh ici", "le CPU tourne à fond"},
			},
			"low": {
				What:     "The low effort (low, faible, bas, minimal, the lowest), as an instruction or a wish, also as a cap or for easier work.",
				NotFor:   "Another level named.",
				Examples: []string{"passe en low", "low is enough for the changelog", "go back to low for this one", "low effort is fine for that summary", "faible suffit pour ça", "mets l'effort à low pour les fichiers de traduction"},
			},
			"medium": {
				What:     "The medium effort (medium, moyen, mid), as an instruction or a wish, also as a cap or for easier work.",
				NotFor:   "Another level named.",
				Examples: []string{"set your effort to medium for the rest", "dial it back to medium", "drop to medium for what's left, it's boilerplate", "medium is enough from here", "repasse en medium pour la suite, c'est mécanique", "moyen suffit pour le reste"},
			},
			"high": {
				What:     "The high effort, named high (or élevé, haut), as an instruction or a wish, also as a cap or for easier work than now.",
				NotFor:   "xhigh or max, named so; more thinking with no level named (more).",
				Examples: []string{"switch to high effort for the migration", "high is fine for the remaining wiring", "high, no more: it's routine", "mets l'effort à high pour la suite", "effort élevé pour ça"},
			},
			"xhigh": {
				What:     "The xhigh effort (extra high, x-high), as an instruction or a wish.",
				NotFor:   "high and max, which are other levels; the ultracode mode (several agents), which is no effort level.",
				Examples: []string{"do this at xhigh", "x-high for this one please", "passe en xhigh", "fais la suite en xhigh"},
			},
			"max": {
				What:     "The max effort (maximum, the highest level), as an instruction or a wish.",
				NotFor:   "xhigh, another level; more thinking with no level named (more).",
				Examples: []string{"max effort on this", "use the maximum reasoning effort for the proof", "fais-le en max", "passe au max pour celle-là"},
			},
			"more": {
				What:     "It asks the assistant to think more than it would, with no level named: think harder, longer or more carefully than usual, take its time, go all out.",
				NotFor:   "A task described as thorough, careful, in depth or harder ('check every path in detail', 'investigate it properly', 'examine the diff closely', 'the next one is harder'): the level judges the task, that is no request. A remark that the level automodel or Jev picked was too low. A level named (low to max); the ultrathink keyword; thinking less.",
				Examples: []string{"think harder about it", "take your time on this one", "think it through more carefully than last time", "réfléchis bien avant de toucher au verrou", "prends ton temps", "mets le paquet sur celle-là"},
			},
		},
	}
	DefaultExplicitMode = catalog.Relation{
		Question: "Does the new prompt `task` ask the assistant itself to do its own work in the {v} mode (several agents working in parallel, orchestrated as a workflow), or to do it without that mode or stop it? Pick what it asks, or none. The request can take any wording, in any language.",
		Options: map[string]*catalog.Option{
			"none": {
				What:     "Neither: most prompts. Also a question, a mention or a complaint about the mode or workflows, turning parallel work on or off for something else (a workflow stage, a config, a subagent), agents that are not the assistant's (CI build agents, support agents), and requests for an effort level or more thinking ('passe en xhigh', 'think harder', 'max effort') or for a model, which are not this mode.",
				NotFor:   "Asking for the mode or several agents doing the assistant's work (on), or asking to work without them (off).",
				Examples: []string{"fix the failing test", "{v} was slow yesterday", "did the workflow finish?", "have one review agent check the migration, then apply its notes yourself", "send a subagent to find which test is flaky and report back", "inutile de changer de modèle, termine la doc", "no need for max effort on the changelog", "disable the parallel stage in the config", "our CI spreads the e2e suite across 4 build agents", "passe en xhigh pour la suite", "think harder about the lock", "should we use {v} for this kind of audit?"},
			},
			"on": {
				What:     "It asks for the mode: {v} by name, a workflow, or several agents or subagents doing the assistant's work in parallel.",
				NotFor:   "One subagent for one task; an effort level or more thinking alone; agents that are not the assistant's; only talking about the mode.",
				Examples: []string{"use {v} for the audit", "fais-le en {v}", "run it with several agents in parallel", "fan this out to subagents", "split the remaining packages across 4 agents", "répartis ça entre plusieurs agents en parallèle", "lance plusieurs sous-agents sur les modules"},
			},
			"off": {
				What:     "It asks the assistant to work without the mode or to stop it, usually doing the work itself, one at a time.",
				NotFor:   "Asking for the mode; turning it off for something else (a config, a workflow stage); refusing an effort level or a model ('pas besoin de max', 'no need for Opus', 'pas besoin de passer sur Sonnet'): only the mode with several agents counts.",
				Examples: []string{"no {v} for this", "skip the workflow, just fix it", "no more agents, do the rest yourself one by one", "single-thread from here", "pas besoin d'{v}", "sans workflow", "arrête les agents en parallèle, fais la suite toi-même"},
			},
		},
	}
	DefaultExplicitModel = catalog.Relation{
		Question: "Does the new prompt `task` ask the assistant itself to run on another model for its own work (this prompt, a part of it, or the rest of the work in progress), instead of the one it runs on now? Pick that model, or none. The request can take any wording, in any language.",
		Options: map[string]*catalog.Option{
			"none": {
				What:     "No model asked for the assistant itself: most prompts. Also talking about models (release news, comparisons, prices, benchmarks, why automodel picked one), setting a model for something else (a subagent, workflow agents or a stage, a config, a tuning or catalog entry), a quoted prompt or test string, a refusal, and requests for an effort level or a mode only.",
				NotFor:   "Asking the assistant to switch to a model, go back to one, or do this work or a part of it with one.",
				Examples: []string{"fix the failing test", "Sonnet 5.5 is out", "is Sonnet cheaper than Opus?", "why did it pick Opus?", "make the review agent use sonnet", "pas besoin d'Opus", "Opus a mis 3 minutes", "passe en xhigh"},
			},
			"model": {
				What:     "{v}: it asks the assistant to switch to {v}, to go back to it, or to do this work, the rest of it or one part of it (a step, a test, a file) with it, as an instruction or a wish, often with the reason.",
				NotFor:   "Another model named; only talking about {v}.",
				Examples: []string{"switch to {v} for this", "do the rest with {v}", "take {v} for this bit, it's routine", "back to {v} for the rest of it", "passe sur {v} pour la suite", "fais ça avec {v}"},
			},
		},
	}
)

// LevelProbs maps a Score answer onto tier IDs (in level order).
func LevelProbs(a Answer, ids []string) map[string]float64 {
	out := make(map[string]float64, len(ids))
	for i, id := range ids {
		out[id] = a.Probabilities[fmt.Sprint(i)]
	}
	return out
}

// Ask sends several questions about one state in one call.
func (cl *Client) Ask(ctx context.Context, model, sessionID string, state any, qs map[string]Question) (map[string]Answer, *Response, error) {
	r, err := cl.do(ctx, Request{Model: model, State: state, SessionID: sessionID, Questions: qs})
	if err != nil {
		return nil, r, err
	}
	for id, q := range qs {
		a, ok := r.Answers[id]
		if !ok {
			return nil, r, fmt.Errorf("jev: no answer for %q", id)
		}
		if q.Type == "noul" && a.Noul == nil {
			return nil, r, fmt.Errorf("jev: %q: noul answer without a value", id)
		}
		if (q.Type == "score" || q.Type == "choice") && len(a.Probabilities) == 0 {
			return nil, r, fmt.Errorf("jev: %q: %s answer without probabilities", id, q.Type)
		}
	}
	return r.Answers, r, nil
}

var ErrNoKey = errors.New("OPENROUTER_API_KEY is not set")

// Decide asks one v1 tier Choice. The context carries the timeout.
func (cl *Client) Decide(ctx context.Context, model, sessionID string, state any, q Question) (*Answer, *Response, error) {
	r, err := cl.do(ctx, Request{Model: model, State: state, SessionID: sessionID, Questions: map[string]Question{QuestionName: q}})
	if err != nil {
		return nil, r, err
	}
	a, ok := r.Answers[QuestionName]
	if !ok || a.Choice == "" {
		return nil, r, fmt.Errorf("jev: no answer for %q", QuestionName)
	}
	if crit, _ := q.Criteria.(map[string]string); crit != nil {
		if _, known := crit[a.Choice]; !known {
			return nil, r, fmt.Errorf("jev: unknown choice %q", a.Choice)
		}
	}
	return &a, r, nil
}

func (cl *Client) do(ctx context.Context, reqBody Request) (*Response, error) {
	if cl.APIKey == "" {
		return nil, ErrNoKey
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cl.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cl.APIKey)
	req.Header.Set("Content-Type", "application/json")
	hc := cl.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var r Response
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("jev: status %d: %.200s", resp.StatusCode, data)
	}
	if r.Error != nil {
		return &r, fmt.Errorf("jev: %d: %s", r.Error.Code, r.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return &r, fmt.Errorf("jev: status %d", resp.StatusCode)
	}
	return &r, nil
}

// Ranked returns options by descending probability.
func (a *Answer) Ranked() []string {
	opts := make([]string, 0, len(a.Probabilities))
	for k := range a.Probabilities {
		opts = append(opts, k)
	}
	sort.Slice(opts, func(i, j int) bool {
		pi, pj := a.Probabilities[opts[i]], a.Probabilities[opts[j]]
		if pi != pj {
			return pi > pj
		}
		return opts[i] < opts[j]
	})
	return opts
}
