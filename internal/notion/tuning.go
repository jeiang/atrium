package notion

import "time"

const (
	TuningNotionBlockDepth               = 5
	TuningNotionBranchLimit              = 50
	TuningNotionHeadSourceChars          = 1000
	TuningNotionLabelChars               = 48
	TuningNotionMaxAliases               = 20
	TuningNotionMaxRetries               = 5
	TuningNotionMaxSources               = 12
	TuningNotionPageSize                 = 100
	TuningNotionRelatedLimit             = 10
	TuningNotionRetryAfterDefaultSeconds = 1.0
	TuningNotionRetryAfterMaxSeconds     = 60.0
	TuningNotionSnippetChars             = 220
	TuningNotionTimeoutSeconds           = 60
)

func DefaultAPILimits() APILimits {
	return APILimits{
		RetryAfterBase: TuningNotionRetryAfterDefaultSeconds,
		RetryAfterMax:  TuningNotionRetryAfterMaxSeconds,
		MaxRetries:     TuningNotionMaxRetries,
		Timeout:        TuningNotionTimeoutSeconds * time.Second,
		PageSize:       TuningNotionPageSize,
		BlockDepth:     TuningNotionBlockDepth,
	}
}

func DefaultSearchLimits() SearchLimits {
	return SearchLimits{
		Branch:     TuningNotionBranchLimit,
		Related:    TuningNotionRelatedLimit,
		Snippet:    TuningNotionSnippetChars,
		HeadSource: TuningNotionHeadSourceChars,
	}
}
