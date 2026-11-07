import type { IntegrationSpec } from '$lib/api/types';

/** The Aadhaar e-KYC example (examples/aadhaar-okyc, int_aadhaar_okyc v1) in contract form. */
export const AADHAAR: IntegrationSpec = {
	targets: [
		{
			vendor_id: 'cashfree-sid',
			account_id: 'cf-sid-test',
			transformer: { request: 'CF-OKYC-OTP-REQ@1', response: 'CF-OKYC-OTP-RES@1' },
			operations: {
				verify_otp: { request: 'CF-OKYC-VERIFY-REQ@1', response: 'CF-OKYC-VERIFY-RES@1' }
			},
			weight: 100
		}
	],
	delivery: { mode: 'sync', on_circuit_open: 'fail_fast' },
	audit: { payloads: 'sealed', retention_ms: 7776000000 }
};
