package sync

import (
	"errors"
	"time"

	belogs "github.com/astaxie/beego/logs"
	conf "github.com/cpusoft/goutil/conf"
	httpclient "github.com/cpusoft/goutil/httpclient"
	jsonutil "github.com/cpusoft/goutil/jsonutil"
	osutil "github.com/cpusoft/goutil/osutil"
	urlutil "github.com/cpusoft/goutil/urlutil"

	"model"
	db "sync/db"
)

var rrdpResultCh chan model.SyncResult
var rsyncResultCh chan model.SyncResult

func init() {
	rrdpResultCh = make(chan model.SyncResult)
	rsyncResultCh = make(chan model.SyncResult)
	belogs.Debug("init(): chan rrdpResultCh:", rrdpResultCh, "   chan rsyncResultCh:", rsyncResultCh)
}

func Start(syncStyle model.SyncStyle) {
	belogs.Info("Start():syncStyle:", syncStyle)

	syncLogSyncState := model.SyncLogSyncState{StartTime: time.Now(), SyncStyle: syncStyle.SyncStyle}

	// start , insert lab_rpki_sync_log
	syncLogId, err := db.InsertSyncLogSyncStateStart("syncing", syncStyle.SyncStyle, &syncLogSyncState)
	if err != nil {
		belogs.Error("Start():InsertSyncLogSyncStateStart fail:", err)
		return
	}
	belogs.Info("Start():syncLogId:", syncLogId, "  syncLogSyncState:", jsonutil.MarshalJson(syncLogSyncState))

	// call tals , get all tals
	talModels, err := getTals()
	if err != nil {
		belogs.Error("Start(): GetTals failed, err:", err)
		return
	}
	belogs.Debug("Start(): len(talModels):", len(talModels))

	// classify rsync and rrdp
	syncLogSyncState.RrdpUrls, syncLogSyncState.RsyncUrls, err = getUrlsBySyncStyle(syncStyle, talModels)
	if err != nil {
		belogs.Error("Start(): getUrlsBySyncType fail")
		return
	}
	belogs.Debug("Start(): rrdpUrls:", syncLogSyncState.RrdpUrls, "   rsyncUrls:", syncLogSyncState.RsyncUrls)

	// Check whether this time sync mode is different from the last sync mode.
	// it means actual directory is different from this sync direcotry.
	// for example, if actual directory is sync , but this time sync mode is rrdp
	// then there must be full sync
	needFullSync, err := checkNeedFullSync(syncLogSyncState.RrdpUrls, syncLogSyncState.RsyncUrls)
	if needFullSync || err != nil {
		belogs.Debug("Start(): checkNeedFullSync fail, rrdpUrls: ", syncLogSyncState.RrdpUrls, "   rsyncUrls:", syncLogSyncState.RsyncUrls, err)
		belogs.Info("Start(): because this time sync mode is different from the last sync mode, so  a full sync has to be triggered")
		go func() {
			httpclient.Post("http", conf.String("rpstir2::sysserver"), conf.Int("rpstir2::httpport"),
				"/sys/initreset", `{"sysStyle":"fullsync", "syncStyle":"`+syncStyle.SyncStyle+`"}`)
		}()
		return
	}

	// call rrdp and rsync and wait for result
	err = callRrdpAndRsync(syncLogId, &syncLogSyncState)
	if err != nil {
		belogs.Error("Start():callRrdpAndRsync fail:", err)
		return
	}
	belogs.Debug("Start(): end callRrdpAndRsync:")

	// update lab_rpki_sync_log
	err = db.UpdateSyncLogSyncStateEnd(syncLogId, "synced", &syncLogSyncState)
	if err != nil {
		belogs.Error("Start():UpdateSyncLogSyncStateEnd fail:", err)
		return
	}

	// will call ChainValidate
	go func() {
		httpclient.Post("http", conf.String("rpstir2::parsevalidateserver"), conf.Int("rpstir2::httpport"),
			"/parsevalidate/start", "")
	}()
}

func getTals() ([]model.TalModel, error) {
	start := time.Now()
	// by /tal/gettals
	resp, body, err := httpclient.Post("http", conf.String("rpstir2::talserver"), conf.Int("rpstir2::httpport"),
		"/tal/gettals", "")
	belogs.Debug("getTals():after /tal/gettals len(body):", len(body))
	if err != nil {
		belogs.Error("getTals(): /tal/gettals connecteds failed, err:", err)
		return nil, err
	}
	defer resp.Body.Close()

	// get parse result
	talResponse := model.TalResponse{}
	jsonutil.UnmarshalJson(string(body), &talResponse)
	if talResponse.HttpResponse.Result != "ok" {
		belogs.Error("getTals(): talResponse failed: Result:", talResponse.HttpResponse.Result)
		return nil, errors.New("get tals failed")
	}
	belogs.Debug("getTals(): talResponse.talModels:", jsonutil.MarshalJson(talResponse.TalModels), "  time(s):", time.Now().Sub(start).Seconds())

	if len(talResponse.TalModels) == 0 {
		belogs.Error("getTals(): there is no tal file")
		return nil, errors.New("there is no tal file")
	}
	return talResponse.TalModels, nil
}

func getUrlsBySyncStyle(syncStyle model.SyncStyle, talModels []model.TalModel) (rrdpUrls, rsyncUrls []string, err error) {
	belogs.Debug("getUrlsBySyncStyle(): syncStyle:", syncStyle, "      talModels:", jsonutil.MarshalJson(talModels))
	for i := range talModels {

		for j := range talModels[i].TalSyncUrls {
			if syncStyle.SyncStyle == "sync" {
				if talModels[i].TalSyncUrls[j].SupportRrdp {
					rrdpUrls = append(rrdpUrls, talModels[i].TalSyncUrls[j].RrdpUrl)
				} else if talModels[i].TalSyncUrls[j].SupportRsync {
					rsyncUrls = append(rsyncUrls, talModels[i].TalSyncUrls[j].RsyncUrl)
				}
			} else if syncStyle.SyncStyle == "rrdp" {
				if talModels[i].TalSyncUrls[j].SupportRrdp {
					rrdpUrls = append(rrdpUrls, talModels[i].TalSyncUrls[j].RrdpUrl)
				}
			} else if syncStyle.SyncStyle == "rsync" {
				if talModels[i].TalSyncUrls[j].SupportRsync {
					rsyncUrls = append(rsyncUrls, talModels[i].TalSyncUrls[j].RsyncUrl)
				}
			}
		}
	}
	belogs.Debug("getUrlsBySyncStyle(): syncStyle:", syncStyle,
		"      rrdpUrls:", rrdpUrls, "  rsyncUrls:", rsyncUrls)

	if len(rrdpUrls) == 0 && len(rsyncUrls) == 0 {
		belogs.Error("getUrlsBySyncType(): there is neighor rrdp urls nor rsync urls")
		return nil, nil, errors.New("there is neighor rrdp urls nor rsync urls")
	}

	return
}

func checkNeedFullSync(thisRrdpUrls, thisRsyncUrls []string) (needFullSync bool, err error) {
	needFullSync = false
	rrdpDestPath := conf.VariableString("rrdp::destpath") + osutil.GetPathSeparator()
	rsyncDestPath := conf.VariableString("rsync::destpath") + osutil.GetPathSeparator()
	belogs.Debug("checkNeedFullSync(): rrdpDestPath,  rsyncDestPath:", rrdpDestPath, rsyncDestPath,
		"  thisRrdpUrls:", thisRrdpUrls, "     thisRsyncUrls:", thisRsyncUrls)

	// if rrdp url exists in sync, or sync url exists in rrdp, it will needFullSync
	for _, thisRrdpUrl := range thisRrdpUrls {
		testRrdpUrlInRsyncDestPath, err := urlutil.JoinPrefixPathAndUrlHost(rsyncDestPath, thisRrdpUrl)
		belogs.Debug("checkNeedFullSync(): test rrdp url in sync:", testRrdpUrlInRsyncDestPath)
		if err != nil {
			belogs.Error("checkNeedFullSync():test rrdp url exists in rsync, JoinPrefixPathAndUrlHost err,  rsyncDestPath, thisRrdpUrl:", rsyncDestPath, thisRrdpUrl)
			return true, err
		}
		exists, err := osutil.IsExists(testRrdpUrlInRsyncDestPath)
		if err != nil {
			belogs.Info("checkNeedFullSync(): test rrdp url exists in rsync, IsExists err, testRrdpUrlInRsyncDestPath:", testRrdpUrlInRsyncDestPath, err)
			return true, err
		}
		if exists {
			belogs.Debug("checkNeedFullSync(): test rrdp url exists in rsync, need full sync:", testRrdpUrlInRsyncDestPath)
			return true, nil
		}
	}
	for _, thisRsyncUrl := range thisRsyncUrls {
		testRsyncUrlInRrdpDestPath, err := urlutil.JoinPrefixPathAndUrlHost(rrdpDestPath, thisRsyncUrl)
		belogs.Debug("checkNeedFullSync(): test rsync url in rrdp:", testRsyncUrlInRrdpDestPath)
		if err != nil {
			belogs.Error("checkNeedFullSync(): test rsync url exists in rrdp, JoinPrefixPathAndUrlHost err,  rrdpDestPath, thisRsyncUrl:", rrdpDestPath, thisRsyncUrl)
			return true, err
		}
		exists, err := osutil.IsExists(testRsyncUrlInRrdpDestPath)
		if err != nil {
			belogs.Error("checkNeedFullSync(): test rsync exists in rrdp, IsExists err, testRsyncUrlInRrdpDestPath:", testRsyncUrlInRrdpDestPath, err)
			return true, err
		}
		if exists {
			belogs.Info("checkNeedFullSync(): test rsync url exits in rrdp ,need full sync:", testRsyncUrlInRrdpDestPath)
			return true, nil
		}
	}
	belogs.Debug("checkNeedFullSync(): not need full sync")
	return false, nil
}

func callRrdpAndRsync(syncLogId uint64, syncLogSyncState *model.SyncLogSyncState) (err error) {

	syncUrls := model.SyncUrls{
		SyncLogId: syncLogId,
		RrdpUrls:  syncLogSyncState.RrdpUrls,
		RsyncUrls: syncLogSyncState.RsyncUrls}
	syncUrlsJson := jsonutil.MarshalJson(syncUrls)
	belogs.Debug("callRrdpAndRsync(): syncUrlsJson:", syncUrlsJson)

	// if there is no rrdp ,then rrdpEnd=true. same to rsyncEnd
	rrdpEnd := false
	rsyncEnd := false
	// will call rrdp and sync
	if len(syncUrls.RrdpUrls) > 0 {
		go func() {
			httpclient.Post("http", conf.String("rpstir2::rrdpserver"), conf.Int("rpstir2::httpport"),
				"/rrdp/start", syncUrlsJson)
		}()
	} else {
		rrdpEnd = true
	}

	if len(syncUrls.RsyncUrls) > 0 {
		go func() {
			httpclient.Post("http", conf.String("rpstir2::rsyncserver"), conf.Int("rpstir2::httpport"),
				"/rsync/start", syncUrlsJson)
		}()
	} else {
		rsyncEnd = true
	}

	// both rrdpEnd==true and rsyncEnd==true, will end select
	belogs.Debug("callRrdpAndRsync(): rrdpEnd, rsyncEnd:", rrdpEnd, rsyncEnd,
		" chan rrdpResultCh:", rrdpResultCh, "   chan rsyncResultCh:", rsyncResultCh)
	for {
		select {
		case syncLogSyncState.RrdpResult = <-rrdpResultCh:
			belogs.Debug("callRrdpAndRsync(): rrdpResult:", jsonutil.MarshalJson(syncLogSyncState.RrdpResult))
			rrdpEnd = true
		case syncLogSyncState.RsyncResult = <-rsyncResultCh:
			belogs.Debug("callRrdpAndRsync(): rsyncResult:", jsonutil.MarshalJson(syncLogSyncState.RsyncResult))
			rsyncEnd = true
		}
		if rrdpEnd && rsyncEnd {
			belogs.Debug("callRrdpAndRsync(): for select  end")
			break
		}
	}
	syncLogSyncState.EndTime = time.Now()
	belogs.Debug("callRrdpAndRsync(): end")
	return
}
func RrdpResult(r *model.SyncResult) {
	belogs.Debug("RrdpResult(): get syncResult:", jsonutil.MarshalJson(*r), "   chan rrdpResultCh:", rrdpResultCh)
	rrdpResultCh <- *r

}
func RsyncResult(r *model.SyncResult) {
	belogs.Debug("RsyncResult(): get syncResult:", jsonutil.MarshalJson(*r), "   chan rsyncResultCh:", rsyncResultCh)
	rsyncResultCh <- *r

}
