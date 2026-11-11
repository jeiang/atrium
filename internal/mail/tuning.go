package mail

import "github.com/jeiang/atrium/internal/endpoint"

const (
	TuningBirthdaysDefaultDays           = 14
	TuningBirthdaysMaxDays               = 366
	TuningDeletionIntervalSeconds        = 86400
	TuningDeletionsMinDeletedShare       = 0.7
	TuningDeletionsMinGroup              = 5
	TuningDeletionsOtpMinShare           = 0.8
	TuningDeletionsOtpOlderThanDays      = 7
	TuningDraftsMaxBody                  = 100000
	TuningDraftsMaxSubject               = 300
	TuningFolderMinMessages              = 10
	TuningGmailLabelsMaxMessages         = 10
	TuningGmailLabelsMinOverlap          = 0.5
	TuningGmailLabelsMinTargetMessages   = 10
	TuningKnnK                           = 7
	TuningKnnMax                         = 4096
	TuningKnnMinShare                    = 0.9
	TuningLlmNeighbors                   = 10
	TuningMailBackoffMaxSeconds          = 300
	TuningMailBackoffStartSeconds        = 5
	TuningMailBodyCapBytes               = 65536
	TuningMailFetchBatch                 = 40
	TuningMailFlagBatch                  = 500
	TuningMailFtsBodyChars               = 20000
	TuningMailIdleSettleSeconds          = 2
	TuningMailScanSampleSubjects         = 5
	TuningMailScanTopLabels              = 5
	TuningMailSearchBranchLimit          = 50
	TuningMailSearchMaxTerms             = 12
	TuningMailSearchRrfK                 = 60
	TuningMailSearchSnippetChars         = 220
	TuningMailSearchSnippetTokens        = 24
	TuningOpenloopsCommitmentScanLimit   = 20
	TuningOpenloopsCommitmentWindowDays  = 14
	TuningOpenloopsCommitmentsPerMessage = 5
	TuningOpenloopsOweMaxAgeDays         = 45
	TuningOpenloopsRemindOverdueDays     = 14
	TuningOpenloopsWaitingMaxAgeDays     = 60
	TuningOpenloopsWindowDays            = 120
	TuningRebalanceClusterIterations     = 15
	TuningRebalanceClusterKs             = "2,3,4"
	TuningRebalanceClusterMargin         = 0.04
	TuningRebalanceClusterMinEmbedded    = 0.8
	TuningRebalanceMergeMinGroup         = 3
	TuningRebalanceMergeMinOthers        = 10
	TuningRebalanceMergeMinShare         = 0.9
	TuningRebalanceMinFolder             = 40
	TuningRebalanceMinGroup              = 20
	TuningRebalanceMinShare              = 0.1
	TuningRebalanceNameMaxChars          = 40
	TuningRebalanceRunGapSeconds         = 1209600
	TuningRebalanceRunsRequired          = 2
	TuningRebalanceTopTerms              = 5
	TuningReportDisagreementLines        = 40
	TuningReportTopDests                 = 15
	TuningRuleMinN                       = 3
	TuningRuleMinPurity                  = 0.9
	TuningSentBackfillDays               = 90
	TuningTriageTopDests                 = 15
	TuningWatchesBodyChars               = 3000
	TuningWatchesPackageWindowDays       = 60
	TuningWatchesScanLimit               = 25
	TuningWatchesStalePackageDays        = 60
	TuningWriteAttempts                  = 3
)

var TuningMailopsBatch = 100

func RebalanceClusterKs() []int {
	out, _ := endpoint.SplitInts(TuningRebalanceClusterKs)
	return out
}
