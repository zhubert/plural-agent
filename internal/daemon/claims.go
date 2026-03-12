package daemon

import (
	"context"
	"os"
	"time"

	"github.com/zhubert/erg/internal/issues"
)

const (
	// claimConsistencyDelay is the wait time after posting a claim before
	// re-reading to verify we won. Allows the provider API to become consistent.
	claimConsistencyDelay = 2 * time.Second

	// claimExpiryBuffer is added to the session's max_duration to produce the
	// claim expiry timestamp. This gives some buffer beyond the expected work
	// duration so the claim doesn't expire while work is still in progress.
	claimExpiryBuffer = 10 * time.Minute
)

// tryClaim attempts to claim an issue for this daemon using the comment-based
// claiming protocol. Returns true if this daemon won the claim (or if the
// provider doesn't support claims), false if another daemon has already claimed it.
//
// The protocol:
//  1. Check for existing non-expired claims — if another daemon has one, skip.
//  2. Post our claim comment with daemon ID, hostname, timestamp, and expiry.
//  3. Wait briefly for API consistency.
//  4. Re-read all claims and verify ours is the earliest non-expired claim.
//  5. If we lost, delete our claim comment and back off.
//
// Error handling is asymmetric by design:
//   - Step 1 fails open (proceed without claiming) because no claim was posted yet.
//   - Step 4 fails closed (delete our claim and skip) because we already posted
//     a claim and can't verify we won — deleting avoids the risk of two daemons
//     both thinking they won due to inconsistent reads.
func (d *Daemon) tryClaim(ctx context.Context, repoPath string, issue issues.Issue, provider issues.Source) (bool, error) {
	cm := d.getClaimManager(provider)
	if cm == nil {
		return true, nil // provider doesn't support claims — proceed
	}

	log := d.logger.With("component", "claim", "issue", issue.ID, "provider", string(provider))

	// 1. Check for existing non-expired claims.
	// Fails open: no claim posted yet, so proceeding is safe.
	existingClaims, err := cm.GetClaims(ctx, repoPath, issue.ID)
	if err != nil {
		log.Warn("failed to read claims, proceeding without claim", "error", err)
		return true, nil
	}

	now := time.Now()
	daemonKey := d.stateKey()

	for _, claim := range existingClaims {
		if claim.DaemonID == daemonKey {
			// Our own claim from a previous run — still valid
			if now.Before(claim.Expires) {
				log.Debug("found our own valid claim, proceeding")
				return true, nil
			}
			// Our own claim expired — delete it and re-claim
			_ = cm.DeleteClaim(ctx, repoPath, issue.ID, claim.CommentID)
			continue
		}
		if now.Before(claim.Expires) {
			// Another daemon has a valid claim
			log.Debug("issue claimed by another daemon", "claimedBy", claim.DaemonID, "expires", claim.Expires)
			return false, nil
		}
		// Expired claim from another daemon — ignore it
	}

	// 2. Post our claim
	hostname, _ := os.Hostname()
	maxDur := time.Duration(d.getMaxDuration()) * time.Minute
	expiry := now.Add(maxDur + claimExpiryBuffer)

	claim := issues.ClaimInfo{
		DaemonID:  daemonKey,
		Hostname:  hostname,
		Timestamp: now,
		Expires:   expiry,
	}

	commentID, err := cm.PostClaim(ctx, repoPath, issue.ID, claim)
	if err != nil {
		log.Warn("failed to post claim, proceeding without", "error", err)
		return true, nil
	}

	// 3. Wait for API consistency
	select {
	case <-time.After(claimConsistencyDelay):
	case <-ctx.Done():
		// Context cancelled during wait — clean up with a fresh context
		// since the original is already done.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = cm.DeleteClaim(cleanupCtx, repoPath, issue.ID, commentID)
		return false, ctx.Err()
	}

	// 4. Re-read claims and determine winner.
	// Fails closed: we already posted a claim, so if we can't verify, delete
	// it rather than risk two daemons both thinking they won.
	allClaims, err := cm.GetClaims(ctx, repoPath, issue.ID)
	if err != nil {
		log.Warn("failed to verify claim, deleting and skipping", "error", err)
		_ = cm.DeleteClaim(ctx, repoPath, issue.ID, commentID)
		return false, nil
	}

	// Find earliest non-expired claim
	var earliest *issues.ClaimInfo
	for i := range allClaims {
		c := &allClaims[i]
		if now.Before(c.Expires) {
			if earliest == nil || c.Timestamp.Before(earliest.Timestamp) {
				earliest = c
			}
		}
	}

	if earliest != nil && earliest.DaemonID == daemonKey {
		log.Info("claimed issue successfully")
		return true, nil
	}

	// We lost (or no valid claims found, which shouldn't happen but handle safely)
	winner := "unknown"
	if earliest != nil {
		winner = earliest.DaemonID
	}
	log.Debug("lost claim race, backing off", "winner", winner)
	_ = cm.DeleteClaim(ctx, repoPath, issue.ID, commentID)
	return false, nil
}

// isClaimedByOther checks whether an issue has a valid (non-expired) claim
// from a different daemon. Used during recovery to skip issues that another
// daemon instance is already working on. Does not post any claims.
func (d *Daemon) isClaimedByOther(ctx context.Context, repoPath string, issue issues.Issue, provider issues.Source) bool {
	cm := d.getClaimManager(provider)
	if cm == nil {
		return false
	}

	claims, err := cm.GetClaims(ctx, repoPath, issue.ID)
	if err != nil {
		return false // fail open
	}

	now := time.Now()
	daemonKey := d.stateKey()

	for _, claim := range claims {
		if claim.DaemonID != daemonKey && now.Before(claim.Expires) {
			return true
		}
	}
	return false
}

// deleteClaimForIssue removes this daemon's claim comment(s) from an issue.
// Used when work is cancelled, fails to start, or the issue is unqueued.
// All errors are silently ignored — claim cleanup is best-effort.
func (d *Daemon) deleteClaimForIssue(ctx context.Context, repoPath string, issueSource issues.Source, issueID string) {
	cm := d.getClaimManager(issueSource)
	if cm == nil {
		return
	}

	claims, err := cm.GetClaims(ctx, repoPath, issueID)
	if err != nil {
		return
	}

	daemonKey := d.stateKey()
	for _, claim := range claims {
		if claim.DaemonID == daemonKey {
			_ = cm.DeleteClaim(ctx, repoPath, issueID, claim.CommentID)
		}
	}
}

// getClaimManager returns the ProviderClaimManager for the given source, or nil
// if the provider is not registered or doesn't support claiming.
func (d *Daemon) getClaimManager(source issues.Source) issues.ProviderClaimManager {
	if d.issueRegistry == nil {
		return nil
	}
	p := d.issueRegistry.GetProvider(source)
	if p == nil {
		return nil
	}
	cm, ok := p.(issues.ProviderClaimManager)
	if !ok {
		return nil
	}
	return cm
}
