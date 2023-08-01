package basic

type RecoverFunc struct {
	RecoverDo func(ierr any)
}

func (rf *RecoverFunc) SetRecover(rdo func(ierr any)) {
	rf.RecoverDo = rdo
}
