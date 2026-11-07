import type { IntegrationSpec } from '$lib/api/types';

/**
 * The Aadhaar e-KYC example (examples/aadhaar-okyc, int_aadhaar_okyc v1) in contract form: two
 * operations, each with the caller's request schema; `send_otp` on the default pair, `verify_otp`
 * on its own.
 */
export const AADHAAR: IntegrationSpec = {
	operations: {
		send_otp: {
			description: 'Send an OTP to the mobile number linked to the Aadhaar number.',
			idempotent: false,
			validate_input: true,
			request_schema: {
				body: {
					type: 'object',
					required: ['aadhaar_number'],
					properties: {
						aadhaar_number: { type: 'string', pattern: '^[0-9]{12}$', 'x-pii': 'government_id' }
					}
				}
			}
		},
		verify_otp: {
			description: 'Verify the OTP and return the e-KYC result.',
			idempotent: false,
			validate_input: true,
			request_schema: {
				body: {
					type: 'object',
					required: ['otp'],
					properties: { otp: { type: 'string', pattern: '^[0-9]{6}$', 'x-pii': 'free_text' } }
				}
			}
		}
	},
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
