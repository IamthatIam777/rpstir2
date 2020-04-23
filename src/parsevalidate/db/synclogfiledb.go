package db

import (
	"time"

	belogs "github.com/astaxie/beego/logs"
	jsonutil "github.com/cpusoft/goutil/jsonutil"
	xormdb "github.com/cpusoft/goutil/xormdb"
	"github.com/go-xorm/xorm"

	parsevalidatemodel "parsevalidate/model"
)

func GetSyncLogFileModelsBySyncLogId(labRpkiSyncLogId uint64) (syncLogFileModels *parsevalidatemodel.SyncLogFileModels, err error) {
	start := time.Now()

	belogs.Debug("GetSyncLogFileModelsBySyncLogId():start")
	dbSyncLogFileModels := make([]parsevalidatemodel.SyncLogFileModel, 0)
	err = xormdb.XormEngine.Table("lab_rpki_sync_log_file").Select("id,syncLogId,filePath,fileName, fileType, syncType").
		Where("state->'$.updateCertTable'=?", "notYet").And("syncLogId=?", labRpkiSyncLogId).
		OrderBy("id").Find(&dbSyncLogFileModels)
	if err != nil {
		belogs.Error("GetSyncLogFileModelsBySyncLogId(): Find fail:", err)
		return nil, err
	}
	belogs.Debug("GetSyncLogFileModelsBySyncLogId(): len(dbSyncLogFileModels):", len(dbSyncLogFileModels))

	var certId uint64
	var tableName string
	for i, _ := range dbSyncLogFileModels {
		// only "update" and "del" have certId
		if dbSyncLogFileModels[i].SyncType == "add" {
			continue
		}
		switch dbSyncLogFileModels[i].FileType {
		case "cer":
			tableName = "lab_rpki_cer"
		case "crl":
			tableName = "lab_rpki_crl"
		case "mft":
			tableName = "lab_rpki_mft"
		case "roa":
			tableName = "lab_rpki_roa"
		}
		has, err := xormdb.XormEngine.Table(tableName).Where("filePath=?", dbSyncLogFileModels[i].FilePath).
			And("fileName=?", dbSyncLogFileModels[i].FileName).Cols("id").Get(&certId)
		if err != nil {
			belogs.Error("GetSyncLogFileModelsBySyncLogId(): get id fail:", tableName,
				dbSyncLogFileModels[i].FilePath, dbSyncLogFileModels[i].FileName, err)
			return nil, err
		}
		if has {
			dbSyncLogFileModels[i].CertId = certId
			belogs.Debug("GetSyncLogFileModelsBySyncLogId():get id: ", tableName,
				dbSyncLogFileModels[i].FilePath, dbSyncLogFileModels[i].FileName, dbSyncLogFileModels[i].CertId)
		}
	}
	syncLogFileModels = parsevalidatemodel.NewSyncLogFileModels(labRpkiSyncLogId, dbSyncLogFileModels)
	belogs.Info("GetSyncLogFileModelsBySyncLogId(): end, len(dbSyncLogFileModels),  time(s):", len(dbSyncLogFileModels), time.Now().Sub(start).Seconds())
	return syncLogFileModels, nil

}

func UpdateSyncLogFilesJsonAllAndState(session *xorm.Session, syncLogFileModels []parsevalidatemodel.SyncLogFileModel) error {
	belogs.Debug("UpdateSyncLogFilesJsonAllAndState(): len(syncLogFileModels):", len(syncLogFileModels))
	sqlStr := `update lab_rpki_sync_log_file f set 	
	  f.state=json_replace(f.state,'$.updateCertTable','finished','$.rtr',?) ,
	  f.jsonAll=?  where f.id=?`
	for i, _ := range syncLogFileModels {
		rtrState := "notNeed"
		if syncLogFileModels[i].FileType == "roa" && syncLogFileModels[i].SyncType != "del" {
			rtrState = "notYet"
		}

		_, err := session.Exec(sqlStr, rtrState, xormdb.SqlNullString(syncLogFileModels[i].JsonAll), syncLogFileModels[i].Id)
		if err != nil {
			belogs.Error("UpdateSyncLogFilesJsonAllAndState(): updateSyncLogFileJsonAllAndState fail:",
				jsonutil.MarshalJson(syncLogFileModels[i]),
				"   syncLogFileId:", syncLogFileModels[i].Id, err)
			return err
		}
	}
	return nil
}
