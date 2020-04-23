package leaderelection
import (
	gocontext "context"
	"encoding/json"
	"fmt"
	"github.com/kubeedge/beehive/pkg/core"
	beehiveContext "github.com/kubeedge/beehive/pkg/core/context"
	"github.com/kubeedge/kubeedge/cloud/pkg/edgecontroller/utils"
	config "github.com/kubeedge/kubeedge/pkg/apis/componentconfig/cloudcore/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/uuid"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"
	componentbaseconfig "k8s.io/component-base/config"
	"k8s.io/klog"
	podutil "k8s.io/kubernetes/pkg/api/v1/pod"
	"os"
	"syscall"
	"time"
)

func Run(cfg *config.CloudCoreConfig, readyzAdaptor *ReadyzAdaptor){
	// To help debugging, immediately log config for LeaderElection
	klog.Infof("Config for LeaderElection : %v",*cfg.LeaderElection)
	// Init Context for leaderElection
	beehiveContext.InitContext(beehiveContext.MsgCtxTypeChannel)

	coreBroadcaster := record.NewBroadcaster()
	cli, err := utils.KubeClient()
	if err != nil {
		klog.Warningf("Create kube client for leaderElection failed with error: %s", err)
		return
	}
	coreRecorder := coreBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "CloudCore"})
	leaderElectionConfig, err := makeLeaderElectionConfig(*cfg.LeaderElection, cli, coreRecorder)

	if err != nil {
		klog.Errorf("couldn't create leaderElectorConfig: %v", err)
		return
	}
	leaderElectionConfig.Callbacks = leaderelection.LeaderCallbacks{
		OnStartedLeading: func(ctx gocontext.Context){
			// Start all modules,
			core.StartModules()
			// Patch PodReadinessGate if program run in pod
			err := TryToPatchPodReadinessGate()
			if err!=nil{
				// Terminate the program gracefully
				klog.Errorf("Error patching pod readinessGate: %v", err)
				TriggerGracefulShutdown()
			}
		},
		OnStoppedLeading: func() {
			// TODO: is it necessary to terminate the program gracefully?
			//klog.Fatalf("leaderlection lost, rudely terminate program")
			klog.Errorf("leaderlection lost, gracefully terminate program")
			// Trigger core.GracefulShutdown()
			TriggerGracefulShutdown()
		},
	}

	leaderElector, err := leaderelection.NewLeaderElector(*leaderElectionConfig)
	// Set readyzAdaptor manually
	readyzAdaptor.SetLeaderElection(leaderElector)
	if err != nil {
		klog.Errorf("couldn't create leader elector: %v", err)
		return
	}

	// Start leaderElection until becoming leader, terminate program if leader lost or context.cancel
	go leaderElector.Run(beehiveContext.GetContext())

	// Monitor system signal and shutdown gracefully and it should be in main gorutine
	core.GracefulShutdown()
	return
}
// makeLeaderElectionConfig builds a leader election configuration. It will
// create a new resource lock associated with the configuration.
func makeLeaderElectionConfig(config componentbaseconfig.LeaderElectionConfiguration, client clientset.Interface, recorder record.EventRecorder) (*leaderelection.LeaderElectionConfig, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("unable to get hostname: %v", err)
	}
	// add a uniquifier so that two processes on the same host don't accidentally both become active
	id := hostname + "_" + string(uuid.NewUUID())

	rl, err := resourcelock.New(config.ResourceLock,
		config.ResourceNamespace,
		config.ResourceName,
		client.CoreV1(),
		client.CoordinationV1(),
		resourcelock.ResourceLockConfig{
			Identity:      id,
			EventRecorder: recorder,
		})
	if err != nil {
		return nil, fmt.Errorf("couldn't create resource lock: %v", err)
	}

	return &leaderelection.LeaderElectionConfig{
		Lock:          rl,
		LeaseDuration: config.LeaseDuration.Duration,
		RenewDeadline: config.RenewDeadline.Duration,
		RetryPeriod:   config.RetryPeriod.Duration,
		WatchDog:      nil,
		Name:          "cloudcore",
	}, nil
}
// Try to patch PodReadinessGate if program runs in pod
func TryToPatchPodReadinessGate()error{
	podname,isInPod :=os.LookupEnv("CLOUDCORE_POD_NAME")
	if isInPod == true {
		namespace := os.Getenv("CLOUDCORE_POD_NAMESPACE")
		klog.Infof("CloudCore is running in pod %v/%v, try to patch PodReadinessGate", namespace, podname)
		//TODO: use specific clients
		cli, err := utils.KubeClient()
		if err != nil {
			return fmt.Errorf("create kube client for patching podReadinessGate failed with error: %v", err)
		}

		//Creat patchBytes
		getPod, err := cli.CoreV1().Pods(namespace).Get(podname, metaV1.GetOptions{})
		originalJson, err := json.Marshal(getPod)
		if err != nil {
			return fmt.Errorf("failed to marshal modified pod %q into JSON: %v", podname, err)
		}
		//Todo: Read PodReadinessGate from CloudCore configuration or env
		condition := corev1.PodCondition{Type: "kubeedge.io/CloudCoreIsLeader", Status: corev1.ConditionTrue}
		podutil.UpdatePodCondition(&getPod.Status, &condition)
		newJson, err := json.Marshal(getPod)
		patchBytes, err := strategicpatch.CreateTwoWayMergePatch(originalJson, newJson, corev1.Pod{})
		if err != nil {
			return fmt.Errorf("failed to create two way merge patch: %v",err)
		}

		//Try to patch
		var isPatchSuccess  = false
		for i:=1;i<=5;i++{
			if _, err = cli.CoreV1().Pods(namespace).Patch( podname, types.StrategicMergePatchType, patchBytes, "status"); err != nil {
				klog.Warningf("Error patching podReadinessGate: kubeedge.io/CloudCoreIsLeader to pod %v through apiserver: %v ,try again, times: %d", podname,err,i)
				time.Sleep(time.Second)
			}else{
				klog.Infof("Successfully patching podReadinessGate: kubeedge.io/CloudCoreIsLeader to pod %q through apiserver", podname)
				isPatchSuccess = true
				break
			}
		}
		if isPatchSuccess != true {
			return fmt.Errorf("failed to patch after 5 attempts")
		}
	}else{
		klog.Infoln("CloudCore is not running in pod")
	}
	return nil
}

// Trigger core.GracefulShutdown()
func TriggerGracefulShutdown(){
	if beehiveContext.GetContext().Err() != nil{
		klog.Errorln("Program is in gracefully shutdown")
		return
	}
	klog.Errorln("Trigger graceful shutdown!")
	p,err := os.FindProcess(syscall.Getpid())
	if err != nil{
		klog.Errorf("Failed to find self process %v",err)
	}
	err = p.Signal(os.Interrupt)
	if  err != nil{
		klog.Errorf("Failed to trigger graceful shutdown: ",err)
	}
}

