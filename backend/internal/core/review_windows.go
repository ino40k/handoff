package core

import "errors"

// Reviewed merging is currently a Linux CLI policy. Desktop keeps exclusive renames.
func (r *Receiver) finishReviewed(t *Transfer) error {
	return errors.New("Linux CLI receive policy is unsupported on Windows")
}
func (r *Receiver) recoverReviewed(t *Transfer) error {
	return errors.New("Linux CLI receive policy is unsupported on Windows")
}
