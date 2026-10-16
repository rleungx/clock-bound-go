//go:build linux && amd64

#include "textflag.h"

TEXT ·readTimestampCounter(SB), NOSPLIT, $0-8
	LFENCE
	RDTSC
	LFENCE
	SHLQ $32, DX
	ORQ AX, DX
	MOVQ DX, ret+0(FP)
	RET
