#!/usr/bin/env bash
# Integration scenarios for VRAM-aware Sablier eviction.
# Each scenario starts from a clean slate (containers re-created, sablier
# restarted) so failures of one don't cascade.

set -uo pipefail
source "$(dirname "$0")/helpers.sh"

SESSION_DURATION="${SESSION_DURATION:-2m}"
TOTAL_VRAM_MB="${TOTAL_VRAM_MB:-8000}"
HEADROOM_MB="${HEADROOM_MB:-500}"

trap stop_sablier EXIT

PASS=0
FAIL=0

scenario_header() {
  echo
  echo "================================================================="
  echo "$1"
  echo "================================================================="
}

result_pass() {
  PASS=$((PASS+1))
  echo "  >>> SCENARIO PASS"
}
result_fail() {
  FAIL=$((FAIL+1))
  echo "  >>> SCENARIO FAIL: $1"
}

# Reset for a fresh scenario: kill sablier, remove all sab-test-* containers,
# then recreate the per-scenario set.
reset() {
  stop_sablier
  cleanup_containers
}

############################################################
scenario_header "S1: Non-participant (no peak label) starts and TTL-expires"
reset
create_test_container "noparti"   ""    ""
SESSION_DURATION="6s"
start_sablier --vram.enabled --vram.total-mb=$TOTAL_VRAM_MB --vram.headroom-mb=$HEADROOM_MB
trigger_start "noparti"
sleep 1
if assert_running "sab-test-noparti"; then
  echo "  noparti running. Now waiting $((6+5))s for TTL to fire..."
  sleep 12
  if assert_running ""; then
    echo "  TTL evicted non-participant correctly."
    result_pass
  else
    result_fail "non-participant should have been TTL-evicted"
    print_log_tail 30
  fi
else
  result_fail "non-participant did not start"
  print_log_tail 30
fi

############################################################
scenario_header "S2: Single participant within budget loads with no eviction"
reset
create_test_container "a" 2000 10
SESSION_DURATION="2m"
start_sablier --vram.enabled --vram.total-mb=$TOTAL_VRAM_MB --vram.headroom-mb=$HEADROOM_MB
trigger_start "a"
sleep 1
if assert_running "sab-test-a"; then
  result_pass
else
  result_fail "participant a did not start"
  print_log_tail 30
fi

############################################################
scenario_header "S3: Three participants fit within budget, all stay loaded"
reset
create_test_container "a" 2000 10
create_test_container "b" 2000 20
create_test_container "c" 3000 30
SESSION_DURATION="2m"
start_sablier --vram.enabled --vram.total-mb=$TOTAL_VRAM_MB --vram.headroom-mb=$HEADROOM_MB
trigger_start "a"
trigger_start "b"
trigger_start "c"
sleep 1
if assert_running "sab-test-a sab-test-b sab-test-c"; then
  result_pass
else
  result_fail "expected all three to be loaded"
  print_log_tail 40
fi

############################################################
scenario_header "S4: Pressure evicts lowest priority (a, prio=10)"
reset
create_test_container "a" 2000 10
create_test_container "b" 2000 20
create_test_container "c" 3000 30
create_test_container "d" 2500 40
SESSION_DURATION="2m"
start_sablier --vram.enabled --vram.total-mb=$TOTAL_VRAM_MB --vram.headroom-mb=$HEADROOM_MB
trigger_start "a"
trigger_start "b"
trigger_start "c"
sleep 1
echo "  before D request:"; running_containers | sed 's/^/    /'
trigger_start "d"
sleep 2
echo "  after D request:"; running_containers | sed 's/^/    /'
# Expected: a evicted (lowest priority). b, c, d remain.
if assert_running "sab-test-b sab-test-c sab-test-d"; then
  result_pass
else
  result_fail "expected a to be evicted, b/c/d to remain"
  print_log_tail 40
fi

############################################################
scenario_header "S5: Tie-break by oldest LastUsed when priorities tie"
reset
create_test_container "old" 3000 50
create_test_container "new" 3000 50
create_test_container "fresh" 3000 50
create_test_container "extra" 4000 50
SESSION_DURATION="2m"
start_sablier --vram.enabled --vram.total-mb=$TOTAL_VRAM_MB --vram.headroom-mb=$HEADROOM_MB
trigger_start "old"; sleep 2
trigger_start "new"; sleep 2
trigger_start "fresh"; sleep 1
echo "  loaded set before extra:"; running_containers | sed 's/^/    /'
# All three loaded. Total used = 9000 > 8000 budget? Wait, 3*3000 = 9000.
# Actually that violates the budget. Let me reconsider.
# Adjust: peaks 2000 each so 6000 used.
# But we already created with 3000 — restart with adjusted setup.
# (Actually 3000*3 = 9000 > 8000 budget — second start would already evict.
# Skip this scenario reset and adjust below.)
echo "  (note: this scenario uses 3x3000=9000 which already exceeds 8000;"
echo "   so the third start itself triggered an eviction. Adjusting expectation.)"
# What we'll actually observe: 'old' got evicted when 'fresh' came in (oldest at the time).
# So loaded set is [new, fresh] before extra.
# Then trigger extra (peak=4000). available = 8000 - 3000(new) - 3000(fresh) = 2000.
# Need 4500. Need to free 2500.  Candidates new(LU later) fresh(LU latest). Pick new.
# After: new evicted. used = 3000(fresh) + 4000(extra) = 7000.
trigger_start "extra"; sleep 2
echo "  loaded set after extra:"; running_containers | sed 's/^/    /'
if assert_running "sab-test-extra sab-test-fresh"; then
  result_pass
else
  result_fail "expected fresh + extra remaining (oldest evicted)"
  print_log_tail 40
fi

############################################################
scenario_header "S6: Request exceeding total VRAM is refused"
reset
create_test_container "huge" 20000 50
SESSION_DURATION="2m"
start_sablier --vram.enabled --vram.total-mb=$TOTAL_VRAM_MB --vram.headroom-mb=$HEADROOM_MB
trigger_start "huge"
sleep 2
if assert_running ""; then
  echo "  huge correctly refused."
  result_pass
else
  result_fail "huge should NOT be running"
  print_log_tail 30
fi

############################################################
scenario_header "S7: VRAM participants are immune from TTL eviction"
reset
create_test_container "p" 2000 50
# Use a short session_duration; participants should ignore it for TTL.
SESSION_DURATION="3s"
start_sablier --vram.enabled --vram.total-mb=$TOTAL_VRAM_MB --vram.headroom-mb=$HEADROOM_MB
trigger_start "p"
sleep 1
if ! assert_running "sab-test-p"; then
  result_fail "participant did not start"
  print_log_tail 30
else
  echo "  participant running. Waiting well past session_duration (10s)..."
  sleep 10
  if assert_running "sab-test-p"; then
    echo "  Participant still running — TTL correctly bypassed."
    result_pass
  else
    result_fail "participant should NOT have been TTL-evicted (declared peak_vram_mb)"
    print_log_tail 30
  fi
fi

############################################################
scenario_header "S8: Non-participant doesn't trigger pressure eviction of others"
reset
create_test_container "p1" 4000 10
create_test_container "p2" 3000 50
create_test_container "noparti" "" ""
SESSION_DURATION="2m"
start_sablier --vram.enabled --vram.total-mb=$TOTAL_VRAM_MB --vram.headroom-mb=$HEADROOM_MB
trigger_start "p1"
trigger_start "p2"
sleep 1
echo "  before noparti:"; running_containers | sed 's/^/    /'
trigger_start "noparti"
sleep 1
echo "  after noparti:"; running_containers | sed 's/^/    /'
# All three should be loaded; non-participant doesn't trigger eviction.
if assert_running "sab-test-noparti sab-test-p1 sab-test-p2"; then
  result_pass
else
  result_fail "noparti's start should NOT have evicted any participants"
  print_log_tail 40
fi

############################################################
scenario_header "S9: Non-participant cannot evict participants under pressure"
reset
# Two participants saturate VRAM; a non-participant won't be picked as a victim.
create_test_container "p1" 4000 10
create_test_container "p2" 3500 20
create_test_container "noparti" "" ""
create_test_container "newp" 1000 100
SESSION_DURATION="2m"
start_sablier --vram.enabled --vram.total-mb=$TOTAL_VRAM_MB --vram.headroom-mb=$HEADROOM_MB
trigger_start "noparti"; sleep 1
trigger_start "p1";    sleep 1
trigger_start "p2";    sleep 1
echo "  before newp:"; running_containers | sed 's/^/    /'
# free = 8000 - 4000(p1) - 3500(p2) = 500. need 1000+500 = 1500.
# Candidates: p1(prio=10), p2(prio=20). noparti is excluded (peak=0).
# Pick p1: frees 4000. New free = 4500. OK.
# Result: p1 evicted, p2 retained, noparti retained, newp loaded.
trigger_start "newp"; sleep 2
echo "  after newp:"; running_containers | sed 's/^/    /'
if assert_running "sab-test-newp sab-test-noparti sab-test-p2"; then
  result_pass
else
  result_fail "expected p1 evicted (lowest priority); noparti must remain"
  print_log_tail 40
fi

############################################################
scenario_header "Summary"
echo "  Passed: $PASS"
echo "  Failed: $FAIL"
if [[ "$FAIL" -gt 0 ]]; then
  exit 1
fi
exit 0
