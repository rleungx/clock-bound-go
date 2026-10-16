//go:build linux && arm64

#include "textflag.h"

TEXT ·readTimestampCounter(SB), NOSPLIT, $0-8
	ISB $15
	MRS CNTVCT_EL0, R0
	ISB $15
	MOVD R0, ret+0(FP)
	RET
