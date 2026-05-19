// Code generated from tools.json contract_version "1.3.0" by gen-go-types.py; DO NOT EDIT.
package mcp

type CreateNoteArgs struct {
	Body string `json:"body,omitempty"`
	CheckDedupBeforeCreate bool `json:"check_dedup_before_create,omitempty"`
	ConfirmDedupOverride bool `json:"confirm_dedup_override,omitempty"`
	Frontmatter map[string]any `json:"frontmatter,omitempty"`
	Reason string `json:"reason,omitempty"`
	Type string `json:"type,omitempty"`
	Zone string `json:"zone,omitempty"`
}

type DeleteNoteArgs struct {
	Confirm bool `json:"confirm,omitempty"`
	Id string `json:"id,omitempty"`
}

type GenerateImageArgs struct {
	AttachTo string `json:"attach_to,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	Style string `json:"style,omitempty"`
}

type GetBacklinksArgs struct {
	Id string `json:"id,omitempty"`
}

type GetCoverageReportArgs struct {
}

type GetMocArgs struct {
	MocId string `json:"moc_id,omitempty"`
}

type GetNoteArgs struct {
	Id string `json:"id,omitempty"`
}

type GetOrphansArgs struct {
	Type string `json:"type,omitempty"`
}

type ListByTagArgs struct {
	Limit int `json:"limit,omitempty"`
	Tag string `json:"tag,omitempty"`
}

type ListByTypeArgs struct {
	Limit int `json:"limit,omitempty"`
	Type string `json:"type,omitempty"`
}

type ListByZoneArgs struct {
	Recursive bool `json:"recursive,omitempty"`
	Zone string `json:"zone,omitempty"`
}

type ListInboxArgs struct {
}

type MergeNotesArgs struct {
	SrcId string `json:"src_id,omitempty"`
	Strategy string `json:"strategy,omitempty"`
	TgtId string `json:"tgt_id,omitempty"`
}

type MoveNoteArgs struct {
	Id string `json:"id,omitempty"`
	TargetPath string `json:"target_path,omitempty"`
}

type RelatedNotesArgs struct {
	Depth int `json:"depth,omitempty"`
	Id string `json:"id,omitempty"`
	Limit int `json:"limit,omitempty"`
}

type RenderCardArgs struct {
	Id string `json:"id,omitempty"`
}

type SearchNotesArgs struct {
	Cloud bool `json:"cloud,omitempty"`
	DateFrom string `json:"date_from,omitempty"`
	DateTo string `json:"date_to,omitempty"`
	Hybrid bool `json:"hybrid,omitempty"`
	Limit int `json:"limit,omitempty"`
	Query string `json:"query,omitempty"`
	Tags []any `json:"tags,omitempty"`
	Type string `json:"type,omitempty"`
	Visibility string `json:"visibility,omitempty"`
	Zone string `json:"zone,omitempty"`
}

type SemanticSimilarArgs struct {
	IdOrText string `json:"id_or_text,omitempty"`
	Limit int `json:"limit,omitempty"`
}

type SummarizePdfArgs struct {
	AsType string `json:"as_type,omitempty"`
	PdfPath string `json:"pdf_path,omitempty"`
}

type TraverseGraphArgs struct {
	Depth int `json:"depth,omitempty"`
	Direction string `json:"direction,omitempty"`
	EdgeTypes []any `json:"edge_types,omitempty"`
	StartId string `json:"start_id,omitempty"`
}

type TriageOverrideArgs struct {
	NewDecision map[string]any `json:"new_decision,omitempty"`
	NoteId string `json:"note_id,omitempty"`
	NotePath string `json:"note_path,omitempty"`
	PreviousDecision map[string]any `json:"previous_decision,omitempty"`
	PreviousDecisionHash string `json:"previous_decision_hash,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type TriageProcessArgs struct {
	InboxId string `json:"inbox_id,omitempty"`
	Related []any `json:"related,omitempty"`
	Tags []any `json:"tags,omitempty"`
	TargetZone string `json:"target_zone,omitempty"`
	Type string `json:"type,omitempty"`
}

type TriageRunArgs struct {
	DryRun bool `json:"dry_run,omitempty"`
	Limit int `json:"limit,omitempty"`
	NoBatch bool `json:"no_batch,omitempty"`
	SessionId string `json:"session_id,omitempty"`
}

type UpdateNoteArgs struct {
	Body string `json:"body,omitempty"`
	FrontmatterPatch map[string]any `json:"frontmatter_patch,omitempty"`
	Id string `json:"id,omitempty"`
}

type ValidateNoteArgs struct {
	Id string `json:"id,omitempty"`
}
