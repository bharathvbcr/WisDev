# Hackathon Submission Status

**Challenge:** Google for Startups AI Agents Challenge  
**Deadline:** June 11, 2026, 7:00 PM CDT  
**Track:** Build (Net-New Agents)

---

## Code & OSS (done)

| Item | Status |
|------|--------|
| WisDev OSS repo published | ✅ https://github.com/bharathvbcr/WisDev |
| MCP tools rebranded (`wisdev*`) | ✅ |
| ADK + MCP + YOLO runtime | ✅ |
| Submission doc package | ✅ `docs/hackathon/*` |
| Live MCP status in ScholarLM UI | ✅ |
| Hackathon landing banner | ✅ Off post-deadline (`hackathonSubmissionBanner: false`) |
| Preflight script | ✅ `npm run ops:hackathon:preflight` |
| Judge user creation script | ✅ `npm run ops:hackathon:judge-user` |
| Diagram export script | ✅ `npm run ops:hackathon:diagram` |

---

## Manual (your action)

| Item | Status | Command / doc |
|------|--------|----------------|
| Record demo video | ⬜ | `DEMO_SCRIPT.md` |
| Upload video (YouTube/Vimeo) | ⬜ | Paste URL in `SUBMISSION.md` |
| Export architecture diagram | ✅ | `assets/architecture-overview.png` via `npm run ops:hackathon:diagram` |
| Create judge Firebase account | ⬜ | `npm run ops:hackathon:judge-user` |
| Fill `JUDGE_ACCESS.md` credentials | ⬜ | |
| Deploy latest ScholarLM frontend | ⬜ | Firebase Hosting / Cloud Run |
| Run preflight before submit | ⬜ | `npm run ops:hackathon:preflight -- --require-judge-auth` |
| Submit Devpost | ⬜ | Copy from `SUBMISSION.md` |

---

## Quick commands

```powershell
cd C:\Users\bhara\Downloads\Code\scholarlm

# Export Devpost-ready architecture PNG/SVG
npm run ops:hackathon:diagram

# Create judge account (requires gcloud ADC + Firebase Auth admin)
$env:HACKATHON_JUDGE_EMAIL = "judge.wisdev2026@yourdomain.com"
$env:HACKATHON_JUDGE_PASSWORD = "your-secure-password"
npm run ops:hackathon:judge-user

# Verify production + judge login + MCP status
npm run ops:hackathon:preflight -- --require-judge-auth
```
